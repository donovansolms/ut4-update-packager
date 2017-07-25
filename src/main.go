package main

import (
	"fmt"

	"github.com/donovansolms/ut4-update-packager/src/packager"
)

func main() {
	fmt.Println("Testing")

	// TODO: Read from environment

	connectionString := fmt.Sprintf("%s:%s@tcp(%s)/%s?%s",
		"root",
		"root",
		"localhost:3306",
		"unattended",
		"charset=utf8&parseTime=True")
	packager := packager.New(
		"http://update.donovansolms.local/temp/utfeed.rss",
		connectionString,
	)

	packager.Run()

}
