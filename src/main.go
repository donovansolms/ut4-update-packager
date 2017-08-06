package main

import (
	"fmt"
	"log"

	"github.com/donovansolms/ut4-update-packager/src/packager"
	"github.com/kelseyhightower/envconfig"
)

// Config holds the configuration information from env vars
type Config struct {
	ReleaseFeedURL   string `split_words:"true"`
	ReleaseDir       string `split_words:"true"`
	WorkingDir       string `split_words:"true"`
	PackageDir       string `split_words:"true"`
	DatabaseUser     string `split_words:"true"`
	DatabasePassword string `split_words:"true"`
	DatabaseName     string `split_words:"true"`
	DatabaseHost     string `split_words:"true"`
	DatabasePort     uint   `split_words:"true"`
}

func main() {
	var config Config
	err := envconfig.Process("packager", &config)
	if err != nil {
		log.Fatal(err.Error())
	}

	connectionString := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		config.DatabaseUser,
		config.DatabasePassword,
		config.DatabaseHost,
		config.DatabasePort,
		config.DatabaseName,
		"charset=utf8&parseTime=True")
	packager, err := packager.New(
		config.ReleaseFeedURL,
		connectionString,
		config.WorkingDir,
		config.ReleaseDir,
		config.PackageDir,
	)
	if err != nil {
		panic(err)
	}

	// TODO: Remove later
	err = packager.Run()
	if err != nil {
		panic(err)
	}
}
