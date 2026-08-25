// Command dashboard renders the UpNext dashboard to the Luckfox framebuffer.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	once := flag.Bool("once", false, "render a single frame and exit")
	fbDev := flag.String("fb", "/dev/fb0", "framebuffer device")
	cfgPath := flag.String("config", "/root/config.json", "path to config.json")
	flag.Parse()

	fmt.Printf("dashboard: once=%v fb=%s config=%s\n", *once, *fbDev, *cfgPath)
	os.Exit(0)
}
