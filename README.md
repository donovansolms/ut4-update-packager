# Unreal Tournament 4 Incremental Update Packager

The Unreal Tournament 4 update packager for Linux

[![Current Version](https://img.shields.io/badge/version-development-orange.svg)](https://img.shields.io/badge/version-development-orange.svg)

## About

The update packager builds incremental updates for every newly released version and makes it available to the [Unreal Tournament 4 Launcher for Linux](https://github.com/donovansolms/ut4-launcher)

## How it works

1. For every new release of UT4 for Linux the update is pulled to the server
2. Hashes are calculated and added to the [update server](https://github.com/donovansolms/ut4-update-server)
3. The new version is made available to the [update server](https://github.com/donovansolms/ut4-update-server)
4. The [launcher](https://github.com/donovansolms/ut4-launcher) will detect the
update and allow you to upgrade your game

## TODO

1. Currently the \*.pak files are by far the largest. A single modified game asset
will require a complete download of the monolithic .pak file (6GB+). I'm working
on a way to solve this as well.

## Contact

You can get in contact on Twitter [@donovansolms](https://twitter.com/donovansolms) or by [creating an issue](https://github.com/donovansolms/ut4-update-packager/issues/new)
