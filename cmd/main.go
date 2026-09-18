package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/db/database"
	"worker-subtitle/internal/desktop"
	"worker-subtitle/internal/subtitle"
)

var version = "dev"

func main() {
	config.Load()
	config.AppConfig.WorkerVersion = version
	var opts subtitle.Options
	noBrowser := flag.Bool("no-browser", true, "do not open dashboard in the default browser (use -no-browser=false to open it)")
	flag.StringVar(&opts.AudioURL, "audio-url", "", "HTTP(S) URL of separated audio")
	flag.StringVar(&opts.AudioFile, "audio-file", "", "local audio file (copied into the test run)")
	flag.StringVar(&opts.MediaID, "media-id", "", "audio media ID; read-only MongoDB lookup")
	flag.StringVar(&opts.OutputDir, "output-dir", config.AppConfig.WorkDir, "parent directory for retained test runs")
	flag.StringVar(&opts.Prompt, "prompt", "", "optional MOSS transcription prompt")
	flag.IntVar(&opts.ChunkSeconds, "chunk-seconds", 180, "audio seconds per chunk (30-180)")
	flag.IntVar(&opts.MaxTokens, "max-new-tokens", 2048, "output tokens per chunk (128-8192)")
	flag.StringVar(&opts.Python, "python", config.AppConfig.Python, "Python executable")
	flag.StringVar(&opts.ModelDir, "model-dir", config.AppConfig.ModelDir, "local MOSS model directory")
	flag.StringVar(&opts.MossScript, "moss-script", config.AppConfig.MossScript, "MOSS service script")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "worker-subtitle %s\nRun without input flags to serve the local dashboard, prepare MOSS, and claim production subtitle jobs.\nThe browser does not open automatically. Production jobs upload subtitles; explicit input flags run local output tests.\n\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.AudioURL == "" && opts.AudioFile == "" && opts.MediaID == "" {
		if err := desktop.Run(ctx, config.AppConfig, opts, !*noBrowser); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if err := opts.Validate(); err != nil {
		log.Print(err)
		flag.Usage()
		os.Exit(2)
	}
	dir, err := subtitle.Run(ctx, opts)
	database.Disconnect()
	if err != nil {
		log.Printf("Test failed: %v\nFiles retained: %s", err, dir)
		os.Exit(1)
	}
	log.Printf("Test complete. Review files in: %s\nNothing uploaded or deleted.", dir)
}
