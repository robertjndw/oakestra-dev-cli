module oak-dev

// 1.24 is the real floor: strings.SplitSeq (internal/target, cmd_status,
// internal/debugstate) landed there. Pinning the patch release the author
// happened to have installed made every older toolchain refuse to build.
go 1.24

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/spf13/cobra v1.10.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.13.0 // indirect
)
