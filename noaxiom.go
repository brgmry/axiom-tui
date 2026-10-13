//go:build noaxiom

package main

import "github.com/brgmry/tailboard/internal/source"

// noaxiomReason is the error an axiom source gets in a noaxiom build.
const noaxiomReason = "this binary was built without the axiom backend (-tags noaxiom)"

func init() {
	source.Unavailable("axiom", noaxiomReason)
}
