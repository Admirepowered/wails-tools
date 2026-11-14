package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"wailstools/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	a, err := app.New()
	if err != nil {
		log.Fatalf("initialise application: %v", err)
	}
	startHidden := a.StartHidden()
	err = wails.Run(&options.App{
		Title:       "WailsTools",
		Width:       1440,
		Height:      920,
		MinWidth:    1080,
		MinHeight:   680,
		StartHidden: startHidden,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: a.MediaHandler(),
		},
		BackgroundColour: &options.RGBA{R: 18, G: 20, B: 26, A: 1},
		OnStartup:        a.Startup,
		OnShutdown:       a.Shutdown,
		OnBeforeClose:    a.OnBeforeClose,
		Bind:             []interface{}{a},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})
	if err != nil {
		log.Fatalf("run application: %v", err)
	}
}
