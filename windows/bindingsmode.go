//go:build !bindings

package main

// isBindingsBuild distinguishes a normal app build from the throwaway binary
// Wails compiles (with `-tags bindings`) and runs to reflect over the bound
// App struct - see bindingsmode_gen.go for that side and main() for what it
// gates.
const isBindingsBuild = false
