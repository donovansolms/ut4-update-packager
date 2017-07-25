package packager

import (
	"errors"
	"os"
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

// Packager handlers packaging operations
type Packager struct {
	// releaseFeedUrl is the feed where new releases are announced
	releaseFeedURL string
	// connectionString is the MySQL-compatible DB connection string
	connectionString string
}

// New creates a new Packager
func New(releaseFeedURL string, connectionString string) *Packager {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "Jan 02 15:04:05",
	})
	return &Packager{
		releaseFeedURL:   releaseFeedURL,
		connectionString: connectionString,
	}
}

// Run executes the main loop to check for new releases and packages
// the release for update
func (packager *Packager) Run() {
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
	db.Close()

	if newReleasePost != nil {
		log.WithFields(log.Fields{
			"title": newReleasePost.Title,
			"guid":  newReleasePost.GUID,
			"date":  newReleasePost.PublishedParsed.Format("2006-01-02 15:04:03"),
		}).Info("New release post is available")

		downloadURL, err := packager.extractUpdateDownloadLinkFromPost(newReleasePost)
		if err != nil {
			log.WithField("err", "extract_download_link").Error(err.Error())
			goto sleep
		}

		log.Warn(downloadURL)
	}

sleep:
	// TODO: Increase
	time.Sleep(time.Second * 1)
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
	if content, ok := releasePost.Extensions["content"]; ok {
		if encoded, ok := content["encoded"]; ok {
			if len(encoded) == 0 {
				return "", errors.New("Encoded content is empty")
			}

			post := encoded[0].Value
			links := xurls.Relaxed.FindAllString(post, -1)
			for _, link := range links {
				originalLink := link
				link = strings.ToLower(link)
				if strings.Contains(link, "client-xan") &&
					strings.Contains(link, "linux") {
					// TODO: Continue here - link extracted, now download
					log.Warn(originalLink)
				}
			}
		}
	}

	//fmt.Println(releasePost.Extensions["content"]["encoded"][0].Value)
	//fmt.Println(releasePost.Content)

	return "", nil
}
