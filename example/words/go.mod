module example.com/words

go 1.27.1

// Only coverage builds (-tags pipelinegen_cover) link it, through cover.go.
require github.com/sideways-works/pipelinegen v0.0.0

// The example lives inside pipelinegen's repository; a repository of its
// own requires a released version instead.
replace github.com/sideways-works/pipelinegen => ../..
