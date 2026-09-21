//go:build bindings

package main

// isBindingsBuild is true only in the throwaway binary Wails compiles with
// `-tags bindings` and runs to dump the bound App struct as JSON
// (wailsjs/go/main/App.js and .d.ts are generated from that output).
//
// main() gates its GUI-startup preflight on this. Without the gate, those
// steps run in the generator's binary too, and any early `return` they take -
// a still-running instance holding the single-instance mutex, or wintun.dll
// failing to be written next to a binary living in a temp directory - is
// invisible to the generator: it sees empty stdout, silently keeps whatever
// App.js already contained, and the build then fails downstream with
// "X is not exported by App.js", pointing at the frontend instead of at the
// real cause. This cost a while to find once; the two build-tagged constants
// are cheap insurance against finding it again.
const isBindingsBuild = true
