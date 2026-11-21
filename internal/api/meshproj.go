package api

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/threemf"
)

// MeshPart is one STL/OBJ file in a mesh project and the plate it sits on.
type MeshPart struct {
	ID    int     `json:"id"`
	File  string  `json:"file"` // stored file name in the project folder
	Name  string  `json:"name"`
	SizeX float64 `json:"size_x_mm"`
	SizeY float64 `json:"size_y_mm"`
	SizeZ float64 `json:"size_z_mm"`
	Plate int     `json:"plate"`
}

// rebuildMesh regenerates the plate view of a mesh project from its parts.
func rebuildMesh(p *Project) {
	if p.PlateCount < 1 {
		p.PlateCount = 1
	}
	d := &threemf.Project{Plates: []threemf.Plate{}, Filaments: []threemf.Filament{{Index: 1, Type: "PLA"}}}
	for n := 1; n <= p.PlateCount; n++ {
		pl := threemf.Plate{Index: n, Name: fmt.Sprintf("Plate %d", n), Parts: []threemf.Part{}, Filaments: []int{1}}
		for _, mp := range p.MeshParts {
			if mp.Plate != n {
				continue
			}
			pl.Parts = append(pl.Parts, threemf.Part{ObjectID: mp.ID, Name: mp.Name, SizeX: mp.SizeX, SizeY: mp.SizeY, SizeZ: mp.SizeZ})
			pl.MaxX = math.Max(pl.MaxX, mp.SizeX)
			pl.MaxY = math.Max(pl.MaxY, mp.SizeY)
			pl.MaxZ = math.Max(pl.MaxZ, mp.SizeZ)
			pl.FootprintMM2 += mp.SizeX * mp.SizeY
		}
		pl.FootprintMM2 = math.Round(pl.FootprintMM2*100) / 100
		d.Plates = append(d.Plates, pl)
	}
	p.Data = d
	p.Size = 0
}

func (a *App) meshInputs(p *Project, plate int) []string {
	var out []string
	for _, mp := range p.MeshParts {
		if mp.Plate == plate {
			out = append(out, filepath.Join(a.projectDir(p.ID), mp.File))
		}
	}
	return out
}

// NewMeshProject creates an empty project with one empty plate.
func (a *App) NewMeshProject(name string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "New project"
	}
	p := &Project{ID: newID(), Name: name, Uploaded: time.Now(), Kind: "mesh", PlateCount: 1, MeshParts: []MeshPart{}}
	rebuildMesh(p)
	_ = os.MkdirAll(a.projectDir(p.ID), 0o755)
	a.projMu.Lock()
	defer a.projMu.Unlock()
	a.projects[p.ID] = p
	return p.clone(), a.saveProjectsLocked()
}

// editMesh runs f on a mesh project under the lock, then rebuilds and saves.
func (a *App) editMesh(id string, f func(p *Project) error) (*Project, error) {
	a.projMu.Lock()
	defer a.projMu.Unlock()
	p, ok := a.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found")
	}
	if p.Slicer != nil {
		return nil, fmt.Errorf("this project's plates were set up in the slicer: change them there (Edit in slicer)")
	}
	if p.Kind != "mesh" {
		return nil, fmt.Errorf("3MF projects keep the plates they were made with")
	}
	if err := f(p); err != nil {
		return nil, err
	}
	rebuildMesh(p)
	return p.clone(), a.saveProjectsLocked()
}

// AddMeshFiles adds STL/OBJ uploads to a plate. plate 0 (or one past the
// last plate) creates a new plate for them.
func (a *App) AddMeshFiles(id string, plate int, ups []Upload) (*Project, error) {
	type measured struct {
		up   Upload
		part threemf.Part
	}
	var ms []measured
	for _, u := range ups {
		if !threemf.MeshFormats[strings.ToLower(filepath.Ext(u.Name))] {
			return nil, fmt.Errorf("%s: only STL and OBJ files can be added to plates", u.Name)
		}
		f, err := os.Open(u.Tmp)
		if err != nil {
			return nil, err
		}
		fi, _ := f.Stat()
		part, err := threemf.ReadMesh(u.Name, f, fi.Size())
		f.Close()
		if err != nil {
			return nil, err
		}
		ms = append(ms, measured{u, part})
	}
	return a.editMesh(id, func(p *Project) error {
		if plate <= 0 || plate > p.PlateCount {
			p.PlateCount++
			plate = p.PlateCount
		}
		next := 1
		for _, mp := range p.MeshParts {
			if mp.ID >= next {
				next = mp.ID + 1
			}
		}
		_ = os.MkdirAll(a.projectDir(p.ID), 0o755)
		for _, m := range ms {
			// Keep the original name (Orca shows it as the object name), made safe and unique.
			file := fmt.Sprintf("%03d_%s%s", next, unsafeName.ReplaceAllString(m.part.Name, "_"), strings.ToLower(filepath.Ext(m.up.Name)))
			dst := filepath.Join(a.projectDir(p.ID), file)
			if err := moveFile(m.up.Tmp, dst); err != nil {
				return err
			}
			_ = os.Chmod(dst, 0o444)
			p.MeshParts = append(p.MeshParts, MeshPart{ID: next, File: file, Name: m.part.Name,
				SizeX: m.part.SizeX, SizeY: m.part.SizeY, SizeZ: m.part.SizeZ, Plate: plate})
			next++
		}
		return nil
	})
}

// MovePart puts a part on another plate (0 = a new plate).
func (a *App) MovePart(id string, partID, plate int) (*Project, error) {
	return a.editMesh(id, func(p *Project) error {
		for i := range p.MeshParts {
			if p.MeshParts[i].ID == partID {
				if plate <= 0 || plate > p.PlateCount {
					p.PlateCount++
					plate = p.PlateCount
				}
				p.MeshParts[i].Plate = plate
				return nil
			}
		}
		return fmt.Errorf("part not found")
	})
}

// RemovePart deletes a part and its file.
func (a *App) RemovePart(id string, partID int) (*Project, error) {
	return a.editMesh(id, func(p *Project) error {
		for i, mp := range p.MeshParts {
			if mp.ID == partID {
				path := filepath.Join(a.projectDir(p.ID), mp.File)
				_ = os.Chmod(path, 0o644)
				_ = os.Remove(path)
				p.MeshParts = append(p.MeshParts[:i], p.MeshParts[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("part not found")
	})
}

// AddPlate appends an empty plate.
func (a *App) AddPlate(id string) (*Project, error) {
	return a.editMesh(id, func(p *Project) error { p.PlateCount++; return nil })
}

// RemovePlate deletes an empty plate and renumbers the ones after it.
func (a *App) RemovePlate(id string, plate int) (*Project, error) {
	return a.editMesh(id, func(p *Project) error {
		if plate < 1 || plate > p.PlateCount {
			return fmt.Errorf("plate %d not found", plate)
		}
		if p.PlateCount == 1 {
			return fmt.Errorf("a project needs at least one plate")
		}
		for _, mp := range p.MeshParts {
			if mp.Plate == plate {
				return fmt.Errorf("move or remove the parts on plate %d first", plate)
			}
		}
		for i := range p.MeshParts {
			if p.MeshParts[i].Plate > plate {
				p.MeshParts[i].Plate--
			}
		}
		p.PlateCount--
		sort.SliceStable(p.MeshParts, func(i, j int) bool { return p.MeshParts[i].Plate < p.MeshParts[j].Plate })
		return nil
	})
}

// RenameProject changes a project's display name.
func (a *App) RenameProject(id, name string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	a.projMu.Lock()
	defer a.projMu.Unlock()
	p, ok := a.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found")
	}
	p.Name = name
	return p.clone(), a.saveProjectsLocked()
}
