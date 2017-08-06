package packager

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/donovansolms/ut4-update-packager/src/packager/models"
	"github.com/jhoonb/archivex"
	"github.com/jinzhu/gorm"
	"github.com/mmcdole/gofeed"
	"github.com/mvdan/xurls"
	log "github.com/sirupsen/logrus"

	// This is how SQL drivers are imported
	_ "github.com/go-sql-driver/mysql"
)

// Packager creates new update packages for releases
type Packager struct {
	// releaseFeedUrl is the feed where new releases are announced
	releaseFeedURL string
	// connectionString is the MySQL-compatible DB connection string
	connectionString string
	// workingDir is the path for download and extract
	workingDir string
	// releaseDir is where the releases are stored with their version numbers
	releaseDir string
	// packageDir is where compressed upgrade packages are stored
	packageDir string
}

// New creates a new instance of Packager
func New(releaseFeedURL string,
	connectionString string,
	workingDir string,
	releaseDir string,
	packageDir string) (*Packager, error) {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "Jan 02 15:04:05",
	})
	err := os.MkdirAll(workingDir, 0755)
	if err != nil {
		return &Packager{}, err
	}
	err = os.MkdirAll(releaseDir, 0755)
	if err != nil {
		return &Packager{}, err
	}
	err = os.MkdirAll(packageDir, 0755)
	if err != nil {
		return &Packager{}, err
	}
	return &Packager{
		releaseFeedURL:   releaseFeedURL,
		connectionString: connectionString,
		workingDir:       workingDir,
		releaseDir:       releaseDir,
		packageDir:       packageDir,
	}, nil
}

// CheckForNewRelease checks if a new release has been announced on
// the UT4 blog and returns the download URL if available with the download
// size
func (packager *Packager) CheckForNewRelease() (string, float64, error) {
	var downloadURL string
	var downloadSize float64
	feed, err := packager.fetchFeed()
	if err != nil {
		return downloadURL, downloadSize, err
	}

	releasePosts, err := packager.extractReleasePosts(feed)
	if err != nil {
		return downloadURL, downloadSize, err
	}

	db, err := gorm.Open("mysql", packager.connectionString)
	if err != nil {
		return downloadURL, downloadSize, err
	}
	defer db.Close()
	var newReleasePost *gofeed.Item
	for _, releasePost := range releasePosts {
		var model models.Ut4BlogPost
		query := db.
			Where("guid = ? AND is_deleted = 0", releasePost.GUID).
			First(&model)
		if query.Error != nil {
			if query.Error == gorm.ErrRecordNotFound {
				// New blog post found
				newReleasePost = releasePost
			} else {
				return downloadURL, downloadSize, query.Error
			}
		}
	}

	log.WithFields(log.Fields{
		"title": newReleasePost.Title,
		"guid":  newReleasePost.GUID,
		"date":  newReleasePost.PublishedParsed.Format("2006-01-02 15:04:03"),
	}).Info("New release post is available")

	// TODO: Send email

	downloadURL, err = packager.extractUpdateDownloadLinkFromPost(newReleasePost)
	if err != nil {
		return downloadURL, downloadSize, err
	}
	downloadSize, err = packager.getDownloadSize(downloadURL)
	if err != nil {
		return downloadURL, downloadSize, err
	}

	return downloadURL, downloadSize, nil
}

// DownloadAndExtract downloads and extracts the release from downloadLink
// and returns the extracted path
func (packager *Packager) DownloadAndExtract(downloadURL string) (string, error) {
	// Download the new release
	downloadFilePath := filepath.Join(packager.workingDir, "newrelease.zip")
	err := packager.downloadFile(downloadFilePath, downloadURL)
	if err != nil {
		return "", err
	}
	log.WithFields(log.Fields{
		"output": downloadFilePath,
	}).Info("Downloaded")

	// Extract the files to be able to determine the version
	extractPath := filepath.Join(packager.workingDir, "newrelease")
	err = packager.extract(extractPath, downloadFilePath)
	if err != nil {
		return "", err
	}
	return extractPath, nil
}

// GetVersionList returns the available installed versions as a list
func (packager *Packager) GetVersionList() ([]string, error) {
	fileInfo, err := os.Stat(packager.releaseDir)
	if err != nil {
		return nil, err
	}
	if fileInfo.IsDir() == false {
		return nil, errors.New("The install path must be a directory")
	}

	files, err := ioutil.ReadDir(packager.releaseDir)
	if err != nil {
		return nil, err
	}

	var versions []string
	for _, file := range files {
		if file.IsDir() {
			versions = append(versions, file.Name())
		}
	}
	return versions, nil
}

// Run executes a continuous loop that checks for updates and packages
// new updates as they become available
func (packager *Packager) Run() error {
	// Is a new release available from the blog?
	downloadURL, downloadSize, err := packager.CheckForNewRelease()
	if err != nil {
		log.WithField("err", "check_for_release").Error(err.Error())
		return err
	}
	log.WithFields(log.Fields{
		"link": downloadURL,
		"size": fmt.Sprintf("%.2fMB", (downloadSize / 1024.00 / 1024.00)),
	}).Info("New release is available")

	// Get the new release
	newReleaseTempPath, err := packager.DownloadAndExtract(downloadURL)
	if err != nil {
		log.WithField("err", "download_extract").Error(err.Error())
		return err
	}
	log.WithFields(log.Fields{
		"output": newReleaseTempPath,
	}).Info("Release downloaded and extracted")

	// Determine version
	newVersion, err := packager.getReleaseNumber(newReleaseTempPath)
	if err != nil {
		// TODO: Possibly check the download file name for the version number
		// TODO: Send email with missing release number
		log.WithField("err", "missing_release_version").Error(err.Error())
		return err
	}
	log.WithField("version", newVersion).Info("Version info found")

	// Now that we have the new release's version, we can move the files
	// there
	newReleasePath := filepath.Join(packager.releaseDir, newVersion)
	os.RemoveAll(newReleasePath)
	err = os.Rename(
		newReleaseTempPath,
		newReleasePath)
	if err != nil {
		// TODO: Send email
		log.WithField("err", "move_temp_to_release").Error(err.Error())
		return err
	}

	versions, err := packager.GetVersionList()
	if err != nil {
		log.WithField("err", "version_list").Error(err.Error())
		return err
	}
	log.WithField("versions", versions).Info("Currently available versions")

	db, err := gorm.Open("mysql", packager.connectionString)
	if err != nil {
		return err
	}
	defer db.Close()
	// Now we build an upgrade path for each version to the new version
	// We do this so that you can upgrade from any verion we have listed
	// to the new one. If we don't have a version listed, you'll download
	// the full latest version
	for _, version := range versions {
		if version >= newVersion {
			log.WithFields(log.Fields{
				"fromVersion": version,
				"toVersion":   newVersion}).Debug("Skipping older or equal version")
			continue
		}

		// First check if this upgrade path has been added to the database already
		var updateCheck models.Ut4UpdatePackages
		query := db.Where("from_version = ? AND to_version = ? ANd is_deleted = 0",
			version,
			newVersion,
		).First(&updateCheck)
		if query.Error != nil {
			if query.Error == gorm.ErrRecordNotFound {
				// continue
			} else {
				return query.Error
			}
		}
		if updateCheck.FromVersion != "" && updateCheck.ToVersion != "" {
			// We have this version already
			log.WithFields(log.Fields{
				"fromVersion": version,
				"toVersion":   newVersion,
			}).Warning("Upgrade already processed")
			continue
		}

		packagePath, err := packager.generateUpgradePath(version, newVersion)
		if err != nil {
			log.WithField("err", "generating_upgrade_path").Error(err.Error())
		}
		log.WithFields(log.Fields{
			"fromVersion": version,
			"toVersion":   newVersion,
			"path":        packagePath,
		}).Info("Upgrade package created")

		// TODO: Package needs to be uploaded somewhere
		err = os.Rename(
			packagePath,
			filepath.Join(packager.packageDir, filepath.Base(packagePath)))
		if err != nil {
			return err
		}

		updatePackage := models.Ut4UpdatePackages{
			FromVersion: version,
			ToVersion:   newVersion,
			// TODO: Implement the update
			UpdateURL:   "http://update.donovansolms.com/3301923-3395761.tar.gz",
			DateCreated: time.Now(),
		}
		query = db.Save(&updatePackage)
		if query.Error != nil {
			return err
		}

	}
	// Clear out the working dir, it will be recreated on startup
	os.RemoveAll(packager.workingDir)
	return nil
}

// generateUpgradePath generates and upgrade package from
// fromVersion to toVersion and returns the path to the upgrade package
func (packager *Packager) generateUpgradePath(
	fromVersion string,
	toVersion string) (string, error) {
	log.WithFields(log.Fields{
		"from": fromVersion,
		"to":   toVersion,
	}).Info("Generating upgrade path")
	if fromVersion == toVersion {
		return "", errors.New("fromVersion and toVersion can't be the same")
	}

	fromVersionHashes, err := packager.getVersionHashes(fromVersion)
	if err != nil {
		return "", err
	}
	toVersionHashes, err := packager.getVersionHashes(toVersion)
	if err != nil {
		return "", err
	}

	deltaOperations := packager.calculateHashDeltaOperations(
		fromVersionHashes,
		toVersionHashes)

	// For each file with the operation 'added' or 'modified' copy the file
	// to the new path for packaging
	// 'Removed' operations will be performed on the client using this delta file
	workingPackagePath := filepath.Join(
		packager.workingDir,
		fmt.Sprintf("%s-package", toVersion))
	for filename, operation := range deltaOperations {
		if operation == deltaOperationAdded || operation == deltaOperationModified {

			// We need to check if this is a pak file, if it is, we need to diff
			// and package it separately to not require a full pak download that
			// consists of multiple GBs of data
			if strings.ToLower(filepath.Ext(filename)) == "pak" &&
				operation == deltaOperationModified {
				log.WithField("pak", filename).Debug("Pak file modified")
				continue
			}
			sourcePath := filepath.Join(packager.releaseDir, toVersion, filename)
			destinationPath := filepath.Join(workingPackagePath, filename)
			err = os.MkdirAll(filepath.Dir(destinationPath), 0755)
			if err != nil {
				return "", err
			}
			err = CopyFile(sourcePath, destinationPath)
			if err != nil {
				return "", err
			}
		}
	}
	// Write a copy of the delta operations to the package
	deltaOperationsBytes, err := json.Marshal(&deltaOperations)
	if err != nil {
		if err != nil {
			return "", err
		}
	}
	err = ioutil.WriteFile(
		filepath.Join(workingPackagePath, "operations.json"),
		deltaOperationsBytes,
		0644)
	if err != nil {
		return "", err
	}

	// Create the compressed package file
	// I'm using archivex since it already does recursive compression of a
	// directory...because I'm lazy
	compressedPath := filepath.Join(
		packager.workingDir, fmt.Sprintf("%s-%s.tar.gz", fromVersion, toVersion))
	tar := new(archivex.TarFile)
	err = tar.Create(compressedPath)
	if err != nil {
		return "", err
	}
	err = tar.AddAll(workingPackagePath, false)
	if err != nil {
		return "", err
	}
	tar.Close()

	return compressedPath, nil
}

// fetchFeed fetches the content from the release feed
func (packager *Packager) fetchFeed() (*gofeed.Feed, error) {
	log.WithField("release_feed", packager.releaseFeedURL).Info("Fetching feed")
	parser := gofeed.NewParser()
	feed, err := parser.ParseURL(packager.releaseFeedURL)
	if err != nil {
		return nil, err
	}
	return feed, nil
}

// extractReleasePosts extracts the release posts from the given feed
// as parsed by FetchFeed
func (packager *Packager) extractReleasePosts(
	feed *gofeed.Feed) ([]*gofeed.Item, error) {
	var items []*gofeed.Item
	for _, item := range feed.Items {
		// The release blog posts usually contain the word release in the title
		if strings.Contains(strings.ToLower(item.Title), "release") {
			items = append(items, item)
		}
	}
	return items, nil
}

// extractUpdateDownloadLinkFromPost extracts the Linux client download
// link from the post content
func (packager *Packager) extractUpdateDownloadLinkFromPost(
	releasePost *gofeed.Item) (string, error) {
	// First get the actual content
	var downloadLink string
	if content, ok := releasePost.Extensions["content"]; ok {
		if encoded, ok := content["encoded"]; ok {
			if len(encoded) == 0 {
				return "", errors.New("Encoded content is empty")
			}
			post := encoded[0].Value
			links := xurls.Relaxed.FindAllString(post, -1)
			// Then find the 'client-xan' links
			for _, link := range links {
				originalLink := link
				link = strings.ToLower(link)
				if strings.Contains(link, "client-xan") &&
					strings.Contains(link, "linux") {
					downloadLink = originalLink
				}
			}
		}
	}
	if downloadLink == "" {
		return "", errors.New("No valid download link found")
	}
	return downloadLink, nil
}

// getDownloadSize returns the size in bytes for the requested download URL
func (packager *Packager) getDownloadSize(url string) (float64, error) {
	// HTTP head requests should return the content-length
	resp, err := http.Head(url)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		// Possibly invalid URL, not found, doesn't support head
		return 0, fmt.Errorf(
			"Non-200 status code returned for download URL: %d", resp.StatusCode)
	}
	size, err := strconv.Atoi(resp.Header.Get("Content-Length"))
	if err != nil {
		return 0, err
	}
	return float64(size), nil
}

// downloadFile downloads the file from downloadLink to outputPath
func (packager *Packager) downloadFile(
	outputPath string, downloadLink string) (err error) {

	output, err := os.OpenFile(
		outputPath,
		os.O_TRUNC|os.O_WRONLY|os.O_CREATE,
		0644)
	if err != nil {
		return err
	}
	defer output.Close()

	resp, err := http.Get(downloadLink)
	fmt.Println(downloadLink)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf(
			"DownloadURL returned %s",
			resp.Status)
	}
	_, err = io.Copy(output, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

// extract extracts the ZIP file to extractPath
func (packager *Packager) extract(extractPath string, zipPath string) error {
	err := os.MkdirAll(extractPath, 0744)
	if err != nil {
		return err
	}
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zipReader.Close()

	for _, zipFile := range zipReader.File {
		zipFileReader, err := zipFile.Open()
		if err != nil {
			return err
		}
		defer zipFileReader.Close()
		outputPath := filepath.Join(extractPath, zipFile.Name)
		if zipFile.FileInfo().IsDir() {
			os.MkdirAll(outputPath, zipFile.Mode())
			continue
		}
		// Create the directory when no separate directory entry exists
		os.MkdirAll(filepath.Dir(outputPath), zipFile.Mode())
		outputFile, err := os.OpenFile(
			outputPath,
			os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
			zipFile.Mode())
		if err != nil {
			return err
		}
		defer outputFile.Close()
		_, err = io.Copy(outputFile, zipFileReader)
		if err != nil {
			return err
		}
	}
	return nil
}

// getReleaseNumber extracts the release version from an UT4 install path
func (packager *Packager) getReleaseNumber(installPath string) (string, error) {
	moduleFile, err := os.Open(
		filepath.Join(installPath,
			"LinuxNoEditor/UnrealTournament/Binaries/Linux",
			"UE4-Linux-Shippingx86_64-unknown-linux-gnu.modules"))
	if err != nil {
		return "", err
	}
	defer moduleFile.Close()

	var module UT4Modules
	err = json.NewDecoder(moduleFile).Decode(&module)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(module.Changelist), nil
}

// getVersionHashes gets the version's hashes or generates them if
// they don't exist
func (packager *Packager) getVersionHashes(
	version string) (map[string]string, error) {
	hashes := make(map[string]string)

	versionPath := filepath.Join(packager.releaseDir, version)
	versionHashPath := filepath.Join(
		packager.releaseDir,
		fmt.Sprintf("%s.hashes", version))
	hashFile, err := ioutil.ReadFile(versionHashPath)
	if err != nil {
		log.WithField("version", version).Debug("No hash file exist, generate")
		// Hash file doesn't exist or we couldn't read it
		hashes, err = packager.generateHashes(versionPath)
		if err != nil {
			return hashes, err
		}
		// Save the cached copy
		var hashJSON []byte
		hashJSON, err = json.Marshal(&hashes)
		if err != nil {
			// Don't worry about the error here, just return the hashes then
			return hashes, nil
		}
		// Ignore the error here, if it fails we'll just try next time
		_ = ioutil.WriteFile(versionHashPath, hashJSON, 0644)
		return hashes, nil
	}
	err = json.Unmarshal(hashFile, &hashes)
	if err != nil {
		return hashes, err
	}
	return hashes, nil
}

// generateHashes generates SHA256 hashes for all the
// files in the given searchPath
func (packager *Packager) generateHashes(
	searchPath string) (map[string]string, error) {

	hashes := make(map[string]string)
	var fileList []string
	err := filepath.Walk(
		searchPath,
		func(path string, fileInfo os.FileInfo, err error) error {
			if fileInfo.IsDir() == false {
				fileList = append(fileList, path)
			}
			return nil
		})
	if err != nil {
		return hashes, err
	}

	// Queue jobs!
	for _, filepath := range fileList {
		fileInfo, err := os.Stat(filepath)
		if err != nil {
			return hashes, err
		}
		usePath := strings.Replace(filepath, searchPath+"/", "", -1)
		if fileInfo.Size() == 0 {
			// HACK: return this hash for a zero-byte file, writer won't write any
			// bytes, no hash generated. Fix sometime.
			hashes[usePath] = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
			continue
		}
		file, err := os.Open(filepath)
		if err != nil {
			return hashes, err
		}
		defer file.Close()
		// Set up an internal hash progress tracker
		hasher := sha256.New()
		_, err = io.Copy(hasher, file)
		if err != nil {
			return hashes, err
		}
		hashes[usePath] = fmt.Sprintf("%x", hasher.Sum(nil))
	}
	return hashes, nil
}

// calculateHashDeltaOperations calculates the operations to be performed
// between two versions
func (packager *Packager) calculateHashDeltaOperations(
	fromVersionHashes map[string]string,
	toVersionHashes map[string]string) map[string]string {

	// This will determine what needs to be done to current
	// Modified, Removed will be done first,
	// Added in pass 2
	delta := make(map[string]string)
	for file, hash := range fromVersionHashes {
		if nextHash, ok := toVersionHashes[file]; ok {
			if nextHash != hash {
				// File has been modified
				delta[file] = deltaOperationModified
			}
		} else {
			// File has been removed
			delta[file] = deltaOperationRemoved
		}
	}
	for file := range toVersionHashes {
		if _, ok := fromVersionHashes[file]; !ok {
			delta[file] = deltaOperationAdded
		}
	}
	return delta
}

// CopyFile copies a file from source to destination and preserves permissions
// This functions has been taken from
// https://www.socketloop.com/tutorials/golang-copy-directory-including-sub-directories-files
// with slight modifications because I am too lazy to build my own
func CopyFile(source string, dest string) (err error) {
	sourcefile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourcefile.Close()

	destfile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destfile.Close()

	_, err = io.Copy(destfile, sourcefile)
	if err == nil {
		sourceinfo, err := os.Stat(source)
		if err != nil {
			os.Chmod(dest, sourceinfo.Mode())
		}
	}
	return
}
