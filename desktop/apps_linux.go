package main

// "Приложения под обход" needs to know which process owns a connection, which
// the Linux build (tunnel in phantomd) doesn't do yet - the UI hides the tile.

const appsSupported = false

func applyAppsPlatform([]SmartApp) {}

func runningApps() []SmartApp { return []SmartApp{} }
