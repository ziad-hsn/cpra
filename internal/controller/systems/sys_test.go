package systems

//
//import (
//	"cpra/internal/controller"
//	"cpra/internal/controller/components"
//	"cpra/internal/loader/loader"
//	"log"
//	"testing"
//)
//
//func TestPulseSystem_SchedulesJobs(t *testing.T) {
//	// Arrange
//	l := loader.NewLoader("yaml", "internal/loader/test.yaml.bak")
//	l.Load()
//	m := l.GetManifest()
//	w, err := controller.NewCPRaWorld(&m)
//	if err != nil {
//		log.Fatal(err)
//	}
//	entity := w.Mapper.CreateEntityFromMonitor(m)
//	w.AddComponent(entity, components.PulseConfig{ /* ... */ })
//	w.AddComponent(entity, components.Status{ /* ... */ })
//	// ... setup as needed
//
//	// Act
//	systems.PulseSystem{}.Update(w) // or .Step(w), etc.
//
//	// Assert
//	// Check if job scheduled, status updated, etc.
//	// Use t.Errorf or testify/assert
//}
