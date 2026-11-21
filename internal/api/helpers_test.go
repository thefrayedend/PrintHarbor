package api

import (
	"os"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/printers"
	"github.com/thefrayedend/printharbor/internal/slicer"
)

func printersPrinter(name, preset string) printers.Printer {
	return printers.Printer{Name: name, Driver: "mock", Address: "60", MachinePreset: preset}
}

func slicerJob(project string, plate int, printer string) slicer.Job {
	return slicer.Job{ProjectID: project, Plate: plate, PrinterID: printer, Machine: "Acme 0.4",
		Process: "0.20mm Standard @Acme", Filaments: []string{"Generic PLA"}}
}

func printersPrinterSecs(name, preset, secs string) printers.Printer {
	return printers.Printer{Name: name, Driver: "mock", Address: secs, MachinePreset: preset}
}

// TestMain runs the tests from a throw-away folder. Starting PrintHarbor writes
// the browser extension's files into an "extension" folder beside it; run from
// here, every test run left a copy behind as internal/api/extension/.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "printharbor-api-test")
	if err == nil {
		err = os.Chdir(dir)
	}
	if err != nil {
		panic(err)
	}
	bedFreshWait = time.Millisecond // tests don't wait for a camera to wake
	code := m.Run()
	os.Chdir(os.TempDir())
	os.RemoveAll(dir)
	os.Exit(code)
}
