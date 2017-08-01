package packager

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/donovansolms/ut4-update-packager/src/packager/models"
	"github.com/mmcdole/gofeed"
	"github.com/mvdan/xurls"
	log "github.com/sirupsen/logrus"

	// This is how SQL drivers are imported
	_ "github.com/go-sql-driver/mysql"
	"github.com/jinzhu/gorm"
)

// OldPackager handlers packaging operations
type OldPackager struct {
	// releaseFeedUrl is the feed where new releases are announced
	releaseFeedURL string
	// connectionString is the MySQL-compatible DB connection string
	connectionString string
	// workingDir is the path for download and extract
	workingDir string
}

// New creates a new OldPackager
func NewOld(releaseFeedURL string,
	connectionString string,
	workingDir string) *OldPackager {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "Jan 02 15:04:05",
	})
	return &OldPackager{
		releaseFeedURL:   releaseFeedURL,
		connectionString: connectionString,
		workingDir:       workingDir,
	}
}

// Run executes the main loop to check for new releases and packages
// the release for update
func (packager *OldPackager) Run() {
	var feed *gofeed.Feed
	var releasePosts []*gofeed.Item
	var db *gorm.DB
	var newReleasePost *gofeed.Item

	feed, err := packager.fetchFeed()
	if err != nil {
		log.WithField("err", "fetch_feed").Error(err.Error())
		goto sleep
	}

	releasePosts, err = packager.extractReleasePosts(feed)
	if err != nil {
		log.WithField("err", "extract_releases").Error(err.Error())
		goto sleep
	}

	db, err = gorm.Open("mysql", packager.connectionString)
	if err != nil {
		log.WithField("err", "db_connect").Fatal(err.Error())
	}
	defer db.Close()
	for _, releasePost := range releasePosts {
		var model models.Ut4BlogPost
		query := db.
			Where("guid = ? AND is_deleted = 0", releasePost.GUID).
			First(&model)
		if query.Error != nil {
			if query.Error == gorm.ErrRecordNotFound {
				// New blog post found
				newReleasePost = releasePost
			}
		}
	}

	if newReleasePost != nil {
		log.WithFields(log.Fields{
			"title": newReleasePost.Title,
			"guid":  newReleasePost.GUID,
			"date":  newReleasePost.PublishedParsed.Format("2006-01-02 15:04:03"),
		}).Info("New release post is available")

		// TODO: Send email

		downloadURL, err := packager.extractUpdateDownloadLinkFromPost(newReleasePost)
		if err != nil {
			log.WithField("err", "extract_download_link").Error(err.Error())
			goto sleep
		}
		downloadSize, err := packager.getDownloadSize(downloadURL)
		if err != nil {
			log.WithField("err", "get_download_size").Error(err.Error())
			goto sleep
		}
		log.WithFields(log.Fields{
			"link": downloadURL,
			"size": fmt.Sprintf("%.2fMB", (downloadSize / 1024.00 / 1024.00)),
		}).Info("Release download link found, downloading...")

		downloadFilePath := filepath.Join(packager.workingDir, "ut4-dl.zip")
		err = packager.downloadFile(downloadFilePath, downloadURL)
		if err != nil {
			log.WithField("err", "download").Error(err.Error())
			goto sleep
		}
		log.WithFields(log.Fields{
			"output": downloadFilePath,
		}).Info("Downloaded")

		// TODO: Add function to generate update package between two versions!

		// Extract the files
		extractPath := filepath.Join(packager.workingDir, "ut4extract")
		err = packager.extract(extractPath, downloadFilePath)
		if err != nil {
			log.WithField("err", "extract").Error(err.Error())
			goto sleep
		}
		// Generate hashes for all the files in the extractPath
		hashes, err := packager.generateHashes(extractPath)
		if err != nil {
			log.WithField("err", "hash").Error(err.Error())
			goto sleep
		}
		_ = hashes

		newVersion, err := packager.getReleaseNumber(extractPath)
		if err != nil {
			// TODO: Possibly check the download file name for the version number
			// TODO: Send email with missing release number
			log.WithField("err", "missing_release_version").Error(err.Error())
			os.Exit(1)
		}

		// Select the last version's hashes which is not this version
		var previousHashVersion models.Ut4VersionHashes
		query := db.
			Where("version != ? AND is_deleted = 0", newVersion).
			Order("version DESC").
			First(&previousHashVersion)
		if query.Error != nil {
			if query.Error == gorm.ErrRecordNotFound {
				// TODO: No previous versions exist, this is then the update package
			}
			log.WithField("err", "no_previous_hashes").Error(query.Error.Error())
			goto sleep
		}

		previousHashes := make(map[string]string)
		err = json.Unmarshal([]byte(previousHashVersion.Hashes), &previousHashes)
		if err != nil {
			// TODO: Send email or something?
			log.WithField("err", "previous_hash_unmarshal").Error(err.Error())
			goto sleep
		}
		// Calculate the delta between the two versions
		delta := packager.calculateHashDeltaOperations(previousHashes, hashes)
		if len(delta) == 0 {
			// Nothing changed?
			// TODO: Log this as a new version to avoid downloading again
			log.WithField("err", "no_changes").Error(err.Error())
			goto sleep
		}

		// Check if the Pak file was modified, if so, diff the pak file
		// and create a separate compressed download for the Pak contents
		// The primary pak path is UnrealTournament/Content/Paks/UnrealTournament.pak
		//var pakDeltaPackagePaths []string
		//for filename, op := range delta {
		//	if strings.ToLower(filepath.Ext(filename)) == ".pak" && op == "modified" {
		//		log.WithField("file", filename).Info("Pak file has been modified")
		// TODO generate new update packages for modified paks
		// TODO: Need previous pak file and new pak file to diff
		//	}
		//}

		// Create a new distribution dir for the package
		upgradePackagePath := filepath.Join(packager.workingDir, newVersion)
		err = os.RemoveAll(upgradePackagePath)
		if err != nil {
			log.WithField("err", "pre_remove_upgrade_path").Error(err.Error())
			goto sleep
		}
		// Then move everything that was added or modified
		upgradeFileCount, byteCount, err := packager.createUpgradeDelta(
			delta, extractPath, upgradePackagePath)
		if err != nil {
			log.WithField("err", "create_upgrade_delta").Error(err.Error())
			goto sleep
		}
		log.WithFields(log.Fields{
			"count": upgradeFileCount,
			"size":  fmt.Sprintf("%.2fMB", (float64(byteCount) / 1024.00 / 1024.00)),
		}).Info("Upgrade package files created")

		// Generate new upgrade hash from delta
		deltaHash := packager.generateDeltaHash(delta)
		log.WithField("hash", deltaHash).Info("Delta upgrade hash generated")

		// TODO: Package new version to tar.gz

		// TODO: Upload upgrade package to cloud storage
		// TODO: Insert new version to database
		// TODO: remove extractPath and all working files
	}

sleep:
	// TODO: Increase
	db.Close()
	time.Sleep(time.Second * 1)
}

// fetchFeed fetches the content from the release feed
func (packager *OldPackager) fetchFeed() (*gofeed.Feed, error) {
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
func (packager *OldPackager) extractReleasePosts(
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
func (packager *OldPackager) extractUpdateDownloadLinkFromPost(
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
func (packager *OldPackager) getDownloadSize(url string) (float64, error) {
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
func (packager *OldPackager) downloadFile(
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
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(output, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

// extract extracts the ZIP file to extractPath
func (packager *OldPackager) extract(extractPath string, zipPath string) error {
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

// generateHashes generates SHA256 hashes for all the files in fileList
func (packager *OldPackager) generateHashes(
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

// getReleaseNumber extracts the release version from an UT4 install path
func (packager *OldPackager) getReleaseNumber(installPath string) (string, error) {
	moduleFile, err := os.Open(
		filepath.Join(installPath,
			"LinuxNoEditor/UnrealTournament/Binaries/Linux",
			"UE4-Linux-Shippingx86_64-unknown-linux-gnu.modules"))
	if err != nil {
		return "", err
	}
	defer moduleFile.Close()

	var module OldUT4Modules
	err = json.NewDecoder(moduleFile).Decode(&module)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(module.Changelist), nil
}

// calculateHashDeltaOperations calculates the operations to be performed
// between two versions
func (packager *OldPackager) calculateHashDeltaOperations(
	current map[string]string,
	next map[string]string) map[string]string {

	// This will determine what needs to be done to current
	// Modified, Removed will be done first,
	// Added in pass 2
	delta := make(map[string]string)
	for file, hash := range current {
		if nextHash, ok := next[file]; ok {
			if nextHash != hash {
				// File has been modified
				delta[file] = "modified"
			}
		} else {
			// File has been removed
			delta[file] = "removed"
		}
	}
	for file := range next {
		if _, ok := current[file]; !ok {
			delta[file] = "added"
		}
	}
	return delta
}

// generateDeltaHash creates a hash from the delta operations
func (packager *OldPackager) generateDeltaHash(
	deltaOperations map[string]string) string {

	hasher := sha256.New()
	keys := make([]string, len(deltaOperations))
	for key := range deltaOperations {
		keys = append(keys, key)
	}
	// maps are randomised at runtime by go, we need to
	// order it to ensure the hashes are always the same for
	// the same operations
	sort.Strings(keys)
	for _, key := range keys {
		hasher.Write([]byte(deltaOperations[key]))
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

// createUpgradeDelta creates a new directory with the added and modified
// files from the new download vs. the previous one
func (packager *OldPackager) createUpgradeDelta(
	delta map[string]string,
	extractPath string,
	upgradePackagePath string) (int, int64, error) {
	var upgradeFileCount int
	var byteCount int64
	for filename, op := range delta {
		if op == "added" || op == "modified" {
			extractedFilePath := filepath.Join(extractPath, filename)
			upgradedFilePath := filepath.Join(upgradePackagePath, filename)
			info, err := os.Stat(extractedFilePath)
			if err == nil {
				err = os.MkdirAll(filepath.Dir(upgradedFilePath), 0744)
				if err != nil {
					return 0, 0, err
				}
				err = os.Rename(extractedFilePath, upgradedFilePath)
				if err != nil {
					return 0, 0, err
				}
				upgradeFileCount++
				byteCount += info.Size()
			}
		} else {
			log.WithFields(log.Fields{
				"file": filename,
				"op":   op,
			}).Debug("File removed")
		}
	}
	return upgradeFileCount, byteCount, nil
}
