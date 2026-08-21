//go:build windows

package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The Windows build script generates payload/kanban.exe before compiling this
// installer. README.txt keeps the embed pattern valid in an ordinary source
// checkout.
//
//go:embed payload/*
var payload embed.FS

var version = "dev"

const (
	appExecutable = "kanban.exe"
	uninstallKey  = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Kanban`
)

func main() {
	var err error
	if hasArgument(os.Args[1:], "--uninstall") {
		err = uninstall()
	} else {
		err = install()
	}
	if err != nil {
		showMessage("Kanban setup could not finish.\n\n"+err.Error(), "Kanban Setup", 0x10)
		os.Exit(1)
	}
}

func install() error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return errors.New("Windows did not provide a Local AppData directory")
	}
	installDir := filepath.Join(localAppData, "Programs", "Kanban")
	dataDir := filepath.Join(localAppData, "Kanban", "data")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		return fmt.Errorf("create application directory: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	application, err := payload.ReadFile("payload/kanban.exe")
	if err != nil {
		return errors.New("the installer payload is incomplete")
	}
	appPath := filepath.Join(installDir, appExecutable)
	_ = runHidden("taskkill.exe", "/IM", appExecutable, "/F")
	time.Sleep(250 * time.Millisecond)
	if err := writeAtomic(appPath, application); err != nil {
		return fmt.Errorf("install Kanban: %w", err)
	}

	uninstallerPath := filepath.Join(installDir, "Uninstall Kanban.exe")
	setupPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate setup program: %w", err)
	}
	if !samePath(setupPath, uninstallerPath) {
		setupBytes, readErr := os.ReadFile(setupPath)
		if readErr != nil {
			return fmt.Errorf("prepare uninstaller: %w", readErr)
		}
		if err := writeAtomic(uninstallerPath, setupBytes); err != nil {
			return fmt.Errorf("install uninstaller: %w", err)
		}
	}

	if err := createShortcuts(appPath, installDir); err != nil {
		return err
	}
	if err := registerUninstaller(installDir, appPath, uninstallerPath, len(application)); err != nil {
		return err
	}
	if err := exec.Command(appPath).Start(); err != nil {
		return fmt.Errorf("start Kanban: %w", err)
	}

	showMessage(
		"Kanban is installed and will open in your browser.\n\n"+
			"On the first visit, create your username and password. "+
			"Kanban will start automatically when you sign in to Windows.",
		"Kanban Setup",
		0x40,
	)
	return nil
}

func uninstall() error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return errors.New("Windows did not provide a Local AppData directory")
	}
	installDir := filepath.Join(localAppData, "Programs", "Kanban")
	dataDir := filepath.Join(localAppData, "Kanban", "data")
	answer := showMessage(
		"Remove the Kanban application?\n\n"+
			"Your tasks and account will be preserved in:\n"+dataDir,
		"Uninstall Kanban",
		0x04|0x20,
	)
	if answer != 6 {
		return nil
	}

	_ = runHidden("taskkill.exe", "/IM", appExecutable, "/F")
	if err := removeShortcuts(); err != nil {
		return err
	}
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallKey)

	showMessage(
		"Kanban has been removed.\n\nYour task data was preserved in:\n"+dataDir,
		"Uninstall Kanban",
		0x40,
	)
	script := "Start-Sleep -Seconds 1; Remove-Item -LiteralPath " +
		powerShellQuote(installDir) + " -Recurse -Force -ErrorAction SilentlyContinue"
	return startHidden(
		"powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-Command", script,
	)
}

func createShortcuts(appPath, installDir string) error {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$shell = New-Object -ComObject WScript.Shell
$target = %s
$working = %s
$items = @(
    @{ Path = (Join-Path ([Environment]::GetFolderPath('Desktop')) 'Kanban.lnk'); Arguments = '' },
    @{ Path = (Join-Path ([Environment]::GetFolderPath('Programs')) 'Kanban.lnk'); Arguments = '' },
    @{ Path = (Join-Path ([Environment]::GetFolderPath('Startup')) 'Kanban.lnk'); Arguments = '--background' }
)
foreach ($item in $items) {
    $shortcut = $shell.CreateShortcut($item.Path)
    $shortcut.TargetPath = $target
    $shortcut.WorkingDirectory = $working
    $shortcut.Arguments = $item.Arguments
    $shortcut.Description = 'Kanban task board'
    $shortcut.IconLocation = $target + ',0'
    $shortcut.Save()
}
`, powerShellQuote(appPath), powerShellQuote(installDir))
	if err := runHidden(
		"powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-Command", script,
	); err != nil {
		return fmt.Errorf("create shortcuts: %w", err)
	}
	return nil
}

func removeShortcuts() error {
	script := `
$items = @(
    (Join-Path ([Environment]::GetFolderPath('Desktop')) 'Kanban.lnk'),
    (Join-Path ([Environment]::GetFolderPath('Programs')) 'Kanban.lnk'),
    (Join-Path ([Environment]::GetFolderPath('Startup')) 'Kanban.lnk')
)
foreach ($item in $items) {
    Remove-Item -LiteralPath $item -Force -ErrorAction SilentlyContinue
}
`
	if err := runHidden(
		"powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-Command", script,
	); err != nil {
		return fmt.Errorf("remove shortcuts: %w", err)
	}
	return nil
}

func registerUninstaller(installDir, appPath, uninstallerPath string, appBytes int) error {
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		uninstallKey,
		registry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("register uninstaller: %w", err)
	}
	defer key.Close()
	values := map[string]string{
		"DisplayName":     "Kanban",
		"DisplayVersion":  version,
		"DisplayIcon":     appPath,
		"InstallLocation": installDir,
		"Publisher":       "Kanban",
		"UninstallString": doubleQuote(uninstallerPath) + " --uninstall",
	}
	for name, value := range values {
		if err := key.SetStringValue(name, value); err != nil {
			return fmt.Errorf("register %s: %w", name, err)
		}
	}
	if err := key.SetDWordValue("NoModify", 1); err != nil {
		return err
	}
	if err := key.SetDWordValue("NoRepair", 1); err != nil {
		return err
	}
	return key.SetDWordValue("EstimatedSize", uint32(appBytes/1024))
}

func doubleQuote(value string) string {
	return string(rune(34)) + value + string(rune(34))
}

func writeAtomic(destination string, content []byte) error {
	temporary := destination + ".new"
	if err := os.WriteFile(temporary, content, 0o700); err != nil {
		return err
	}
	_ = os.Remove(destination)
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func samePath(left, right string) bool {
	leftPath, leftErr := filepath.Abs(left)
	rightPath, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(leftPath, rightPath)
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func hasArgument(args []string, wanted string) bool {
	for _, argument := range args {
		if strings.EqualFold(argument, wanted) {
			return true
		}
	}
	return false
}

func runHidden(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%s: %w", message, err)
		}
	}
	return err
}

func startHidden(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return command.Start()
}

func showMessage(message, title string, flags uintptr) int {
	messagePointer, messageErr := windows.UTF16PtrFromString(message)
	titlePointer, titleErr := windows.UTF16PtrFromString(title)
	if messageErr != nil || titleErr != nil {
		return 0
	}
	procedure := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	result, _, _ := procedure.Call(
		0,
		uintptr(unsafe.Pointer(messagePointer)),
		uintptr(unsafe.Pointer(titlePointer)),
		flags,
	)
	return int(result)
}
