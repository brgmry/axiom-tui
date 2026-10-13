//go:build !noaxiom

package main

// The Axiom backend registers the "axiom" source type from init. Build with
// -tags noaxiom to leave it, and with it the axiom-go SDK and
// OpenTelemetry, out of the binary.
import _ "github.com/brgmry/tailboard/internal/source/axiom"
