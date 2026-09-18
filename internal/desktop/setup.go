package desktop

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/subtitle"
	"worker-subtitle/moss"
)

type commandRunner func(context.Context, string, ...string) error

func command(output io.Writer) commandRunner {
	return func(ctx context.Context, program string, args ...string) error {
		started := time.Now()
		defer func() {
			fmt.Fprintf(output, "[setup] %s check finished in %.1fs\n", filepath.Base(program), time.Since(started).Seconds())
		}()
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Stdout = output
		cmd.Stderr = output
		cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8", "PIP_NO_INPUT=1")
		cmd.WaitDelay = 5 * time.Second
		hideWindow(cmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", filepath.Base(program), err)
		}
		return nil
	}
}

func setup(ctx context.Context, cfg config.Config, opts subtitle.Options, report func(string), output io.Writer) (subtitle.Runtime, error) {
	run := command(output)
	report("Unpacking bundled MOSS source")
	if err := moss.Install(filepath.Join(cfg.RuntimeDir, "moss")); err != nil {
		return nil, err
	}
	report("Checking FFmpeg and FFprobe")
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := run(ctx, name, "-version"); err != nil {
			return nil, fmt.Errorf("install %s and add it to PATH, then retry: %w", name, err)
		}
	}
	report("Checking Python environment")
	if err := ensurePython(ctx, cfg, opts.Python, run); err != nil {
		return nil, err
	}
	report("Checking / installing MOSS dependencies")
	if err := ensurePackages(ctx, cfg, opts.Python, run); err != nil {
		return nil, err
	}
	report("Checking CUDA GPU")
	report("Checking / downloading MOSS model")
	script := filepath.Join(cfg.RuntimeDir, "moss", "download_model.py")
	if err := run(ctx, opts.Python, script, "--output", opts.ModelDir, "--check"); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := run(ctx, opts.Python, script, "--output", opts.ModelDir, "--revision", cfg.ModelRevision); err != nil {
			return nil, err
		}
	}
	report("Loading MOSS onto GPU")
	return subtitle.StartRuntime(ctx, opts, output)
}

func ensurePython(ctx context.Context, cfg config.Config, target string, run commandRunner) error {
	check := `import sys; assert (3,10) <= sys.version_info[:2] < (3,14), 'Python 3.10-3.13 required'`
	if _, err := os.Stat(target); err == nil {
		return run(ctx, target, "-c", check)
	}
	if runtime.GOOS == "windows" && cfg.BasePython == "" {
		return ensureWindowsPython(ctx, cfg.RuntimeDir, target, run)
	}
	candidates := []string{cfg.BasePython}
	if cfg.BasePython == "" {
		candidates = []string{"python3", "python"}
		if runtime.GOOS == "windows" {
			candidates = []string{"py", "python"}
		}
	}
	for _, base := range candidates {
		args := []string{}
		if base == "py" {
			found := false
			for _, version := range []string{"-3.12", "-3.13", "-3.11", "-3.10"} {
				if err := run(ctx, base, version, "-c", check); err == nil {
					args = []string{version}
					found = true
					break
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			if !found {
				continue
			}
		} else if err := run(ctx, base, "-c", check); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		// The managed environment always lives next to the binary. A custom missing
		// MOSS_PYTHON path is a configuration error, not a destination to overwrite.
		root := filepath.Join(cfg.RuntimeDir, ".venv")
		expected := filepath.Join(root, "bin", "python")
		if runtime.GOOS == "windows" {
			expected = filepath.Join(root, "Scripts", "python.exe")
		}
		a, _ := filepath.Abs(target)
		b, _ := filepath.Abs(expected)
		if a != b {
			return fmt.Errorf("configured MOSS_PYTHON does not exist: %s", target)
		}
		if err := run(ctx, base, append(args, "-m", "venv", root)...); err != nil {
			return err
		}
		return run(ctx, target, "-c", check)
	}
	return fmt.Errorf("Python 3.10-3.13 not found; install Python or set MOSS_BASE_PYTHON, then click Retry")
}

// Install an unregistered runtime beside the executable, without modifying PATH
// or the user's global Python installation. Reuse it when creating a new venv.
func ensureWindowsPython(ctx context.Context, root, target string, run commandRunner) error {
	venv := filepath.Join(root, ".venv")
	if filepath.Clean(target) != filepath.Join(venv, "Scripts", "python.exe") {
		return fmt.Errorf("managed Python must be inside the executable directory")
	}
	baseDir := filepath.Join(root, "python")
	base := filepath.Join(baseDir, "python.exe")
	check := `import sys; assert sys.version_info[:2] == (3,13), 'Managed Python 3.13 required'`
	if _, err := os.Stat(base); os.IsNotExist(err) {
		if err := run(ctx, "py", "install", "--yes", "--target="+baseDir, "3.13"); err != nil {
			return fmt.Errorf("download Python into %s failed; Python Install Manager (py install) and internet access are required: %w", baseDir, err)
		}
	} else if err != nil {
		return err
	}
	if err := run(ctx, base, "-c", check); err != nil {
		return err
	}
	if err := run(ctx, base, "-m", "venv", venv); err != nil {
		return err
	}
	return run(ctx, target, "-c", check)
}

func ensurePackages(ctx context.Context, cfg config.Config, python string, run commandRunner) error {
	torchCheck := `import torch; assert torch.version.cuda, 'CUDA PyTorch build required'`
	if err := run(ctx, python, "-c", torchCheck); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := run(ctx, python, "-m", "pip", "install", "--upgrade", "torch", "--index-url", cfg.TorchIndex); err != nil {
			return err
		}
	}
	// Validate installed versions without importing the entire model stack again.
	// StartRuntime imports these modules and must succeed before readiness is set.
	check := `from importlib.metadata import version; from packaging.requirements import Requirement; import pathlib; reqs=[Requirement(s.strip()) for s in pathlib.Path(__import__('sys').argv[1]).read_text().splitlines() if s.strip() and not s.lstrip().startswith('#')]; assert all(r.specifier.contains(version(r.name)) for r in reqs), 'MOSS dependencies need updating'`
	requirements := filepath.Join(cfg.RuntimeDir, "moss", "requirements.txt")
	if err := run(ctx, python, "-c", check, requirements); err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := run(ctx, python, "-m", "pip", "install", "-r", requirements); err != nil {
		return err
	}
	return run(ctx, python, "-c", check, requirements)
}
