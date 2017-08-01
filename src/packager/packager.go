package packager

import (
	"archive/zip"
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

	"github.com/donovansolms/ut4-update-packager/src/packager/models"
	"github.com/jinzhu/gorm"
	"github.com/mmcdole/gofeed"
	"github.com/mvdan/xurls"
	log "github.com/sirupsen/logrus"
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
}

// New creates a new instance of Packager
func New(releaseFeedURL string,
	connectionString string,
	workingDir string,
	releaseDir string) *Packager {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "Jan 02 15:04:05",
	})
	return &Packager{
		releaseFeedURL:   releaseFeedURL,
		connectionString: connectionString,
		workingDir:       workingDir,
		releaseDir:       releaseDir,
	}
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
	newReleasePath, err := packager.DownloadAndExtract(downloadURL)
	if err != nil {
		log.WithField("err", "download_extract").Error(err.Error())
		return err
	}
	log.WithFields(log.Fields{
		"output": newReleasePath,
	}).Info("Release downloaded and extracted")

	// Determine version
	newVersion, err := packager.getReleaseNumber(newReleasePath)
	if err != nil {
		// TODO: Possibly check the download file name for the version number
		// TODO: Send email with missing release number
		log.WithField("err", "missing_release_version").Error(err.Error())
		return err
	}
	log.WithField("version", newVersion).Info("Version info found")

	versions, err := packager.GetVersionList()
	if err != nil {
		log.WithField("err", "version_list").Error(err.Error())
		return err
	}
	log.WithField("versions", versions).Info("Currently available versions")

	return nil
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

	var module OldUT4Modules
	err = json.NewDecoder(moduleFile).Decode(&module)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(module.Changelist), nil
}
