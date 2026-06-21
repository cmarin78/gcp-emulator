// spend.go cierra la brecha de Fase 11 para este servicio: en vez de que
// los ThresholdRules de un budget sean metadata que nunca se evalúa, getBudget
// y listBudgets ahora calculan un gasto estimado real a partir de las
// instancias de Compute que existen en los proyectos del budgetFilter (más
// instancias, o instancias más grandes, acumulan gasto más rápido; borrarlas
// lo frena), y cuando ese gasto cruza un thresholdPercent por primera vez se
// deja un log real en Cloud Logging (vía internal/activity) en vez de no
// hacer nada -- el mismo patrón que cloudscheduler/cloudtasks/pubsub usan
// para "esto pasó de verdad" en este proyecto.
//
// No se modela el pricing real de GCP (sería un proyecto en sí mismo): la
// tabla de tarifas por familia de machine type es deliberadamente
// aproximada, solo para que el gasto responda a uso real.
package billingbudgets

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/cesar/gcp-emulator/internal/activity"
	"github.com/cesar/gcp-emulator/internal/storage"
)

// bucketComputeInstances debe coincidir exactamente con el nombre de bucket
// que internal/services/compute usa para sus instancias (bucketInstances en
// compute.go). Se duplica el nombre en vez de importar ese paquete para
// evitar cualquier riesgo de ciclo de imports, la misma técnica que
// internal/iamenforce e internal/activity ya usan en esta fase.
const bucketComputeInstances = "compute.instances"

// computeInstance es una copia local mínima de la forma JSON de
// compute.Instance: solo los campos necesarios para estimar costo.
type computeInstance struct {
	MachineType       string `json:"machineType"`
	CreationTimestamp string `json:"creationTimestamp"`
	SelfLink          string `json:"selfLink"`
}

// hourlyRateUSD es una tabla de tarifa por hora deliberadamente aproximada
// por familia de machine type -- alcanza para que el gasto acumulado
// dependa de uso real sin pretender reproducir el pricing real de GCP.
var hourlyRateUSD = map[string]float64{
	"e2":  0.03,
	"f1":  0.02,
	"g1":  0.025,
	"n1":  0.05,
	"n2":  0.06,
	"n2d": 0.055,
	"c2":  0.09,
	"m1":  0.30,
}

const defaultHourlyRateUSD = 0.04

func rateForMachineType(machineType string) float64 {
	family := machineType
	if idx := strings.IndexByte(machineType, '-'); idx >= 0 {
		family = machineType[:idx]
	}
	if r, ok := hourlyRateUSD[family]; ok {
		return r
	}
	return defaultHourlyRateUSD
}

// projectOfSelfLink extrae {project} de un selfLink "(.../)projects/{project}/...".
func projectOfSelfLink(selfLink string) string {
	const marker = "/projects/"
	idx := strings.Index(selfLink, marker)
	if idx < 0 {
		return ""
	}
	rest := selfLink[idx+len(marker):]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return rest[:end]
	}
	return rest
}

// estimateSpendUSD suma el costo estimado (tarifa por hora * horas desde la
// creación) de cada instancia de Compute cuyo proyecto esté en projects.
// Lee directamente el bucket de compute en vez de importar ese paquete (ver
// comentario de bucketComputeInstances). projects vacío significa "ningún
// proyecto en el filtro del budget" -- un budget sin proyectos especificados
// reales aplica a toda la cuenta de billing, pero ese caso queda fuera de
// alcance aquí (no hay forma de mapear "toda la cuenta" a un set de
// proyectos sin un registro cuenta->proyectos que este emulador no tiene).
func estimateSpendUSD(db *storage.DB, projects []string) float64 {
	if len(projects) == 0 {
		return 0
	}
	want := make(map[string]bool, len(projects))
	for _, p := range projects {
		// budgetFilter.projects usa la forma "projects/{project_id}" en la
		// API real (igual que el test de este paquete: "projects/proj1"),
		// mientras que projectOfSelfLink devuelve el ID pelado -- se
		// normaliza acá para que ambos lados comparen lo mismo.
		want[strings.TrimPrefix(p, "projects/")] = true
	}
	var total float64
	_ = db.List(bucketComputeInstances, "", func(_ string, raw []byte) error {
		var inst computeInstance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return nil
		}
		if !want[projectOfSelfLink(inst.SelfLink)] {
			return nil
		}
		created, err := time.Parse(time.RFC3339, inst.CreationTimestamp)
		if err != nil {
			return nil
		}
		hours := time.Since(created).Hours()
		if hours < 0 {
			hours = 0
		}
		total += rateForMachineType(inst.MachineType) * hours
		return nil
	})
	return total
}

// moneyToUSD convierte un BudgetAmount.SpecifiedAmount a un float64 en su
// moneda (se asume USD, como todo lo demás en este helper -- el emulador no
// modela tasas de cambio).
func moneyToUSD(m *Money) float64 {
	if m == nil {
		return 0
	}
	units, _ := strconv.ParseFloat(m.Units, 64)
	return units + float64(m.Nanos)/1e9
}

// evaluateSpend calcula el gasto estimado actual de un budget y, para cada
// thresholdRule que se cruza por primera vez, deja un log real en Cloud
// Logging vía internal/activity -- la parte observable de "esto realmente
// pasó" sin necesidad de exponer un campo no estándar en la forma de
// Budget (la API real no devuelve gasto en este recurso; el gasto se
// consulta vía BigQuery billing export, fuera de alcance de este
// emulador).
func (s *Service) evaluateSpend(budgetKey string, b Budget) float64 {
	var projects []string
	if b.BudgetFilter != nil {
		projects = b.BudgetFilter.Projects
	}
	spend := estimateSpendUSD(s.db, projects)
	if len(b.ThresholdRules) == 0 || len(projects) == 0 {
		return spend
	}
	amount := moneyToUSD(specifiedAmountOf(b.Amount))
	if amount <= 0 {
		return spend
	}
	project := strings.TrimPrefix(projects[0], "projects/")
	for _, rule := range b.ThresholdRules {
		// thresholdPercent es una fracción 1.0-based (0.5 = 50%), igual que
		// en la API real -- billingbudgets_test.go ya lo usa así (0.9).
		threshold := amount * rule.ThresholdPercent
		if threshold <= 0 || spend < threshold {
			continue
		}
		if s.alreadyNotified(budgetKey, rule.ThresholdPercent) {
			continue
		}
		activity.RecordLog(project, activity.LogEntry{
			LogName:  "projects/" + project + "/logs/billingbudgets.googleapis.com%2Fthreshold",
			Severity: "WARNING",
			TextPayload: "budget " + b.DisplayName + " cruzó el " +
				strconv.FormatFloat(rule.ThresholdPercent*100, 'f', -1, 64) +
				"% de su monto presupuestado (gasto estimado actual: " +
				strconv.FormatFloat(spend, 'f', 2, 64) + ")",
			Resource: map[string]any{"type": "billing_budget", "labels": map[string]string{"budget": b.Name}},
		})
		s.markNotified(budgetKey, rule.ThresholdPercent)
	}
	return spend
}

func specifiedAmountOf(a *BudgetAmount) *Money {
	if a == nil {
		return nil
	}
	return a.SpecifiedAmount
}

func (s *Service) alreadyNotified(budgetKey string, threshold float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notified[budgetKey] != nil && s.notified[budgetKey][threshold]
}

func (s *Service) markNotified(budgetKey string, threshold float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notified == nil {
		s.notified = map[string]map[float64]bool{}
	}
	if s.notified[budgetKey] == nil {
		s.notified[budgetKey] = map[float64]bool{}
	}
	s.notified[budgetKey][threshold] = true
}
