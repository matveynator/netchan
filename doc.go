// Package netchan provides channel-like APIs for exchanging typed values over network connections.
//
// The package exposes two levels of abstraction:
//   - Dial/Listen for generic payloads via chan interface{}.
//   - AdvancedDial/AdvancedListen for explicit Message routing.
//
// Netchan is designed around goroutines and channels to keep the API idiomatic for Go codebases.
package netchan
