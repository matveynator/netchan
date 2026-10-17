// Package interop contains schemas shared by compiler interoperability fixtures.
package interop

type Task struct {
	Name  string
	Reply chan<- string
	Stop  <-chan struct{}
}
