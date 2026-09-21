// This directory holds task definitions, not source code that belongs to the
// trainer. The starter, tests and solution directories are three different
// versions of the same package and do not compile together, so a nested
// module keeps them out of the parent's ./... - the standard way to exclude a
// tree from the go tool.
//
// The runner never uses this file: each run gets a freshly generated go.mod
// in its own temporary directory.
module drill.local/tasks

go 1.24
