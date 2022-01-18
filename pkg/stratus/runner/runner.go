package runner

import (
	"errors"
	"github.com/datadog/stratus-red-team/internal/providers"
	"github.com/datadog/stratus-red-team/internal/state"
	"github.com/datadog/stratus-red-team/internal/utils"
	"github.com/datadog/stratus-red-team/pkg/stratus"
	"log"
	"path/filepath"
)

type Runner struct {
	Technique        *stratus.AttackTechnique
	TechniqueState   stratus.AttackTechniqueState
	TerraformDir     string
	ShouldCleanup    bool
	ShouldWarmUp     bool
	ShouldForce      bool
	TerraformManager TerraformManager
	StateManager     state.StateManager
}

func NewRunner(technique *stratus.AttackTechnique, warmup bool, cleanup bool, force bool) Runner {
	stateManager := state.NewFileSystemStateManager(technique)
	runner := Runner{
		Technique:        technique,
		ShouldWarmUp:     warmup,
		ShouldCleanup:    cleanup,
		ShouldForce:      force,
		TerraformManager: NewTerraformManager(filepath.Join(stateManager.GetRootDirectory(), "terraform")),
		StateManager:     stateManager,
	}
	runner.initialize()

	return runner
}

func (m *Runner) initialize() {
	m.ValidatePlatformRequirements()
	m.TerraformDir = filepath.Join(m.StateManager.GetRootDirectory(), m.Technique.ID)
	m.TechniqueState = m.StateManager.GetTechniqueState()
	if m.TechniqueState == "" {
		m.TechniqueState = stratus.AttackTechniqueStatusCold
	}
}

func (m *Runner) WarmUp() (map[string]string, error) {
	// No pre-requisites to spin-up
	if m.Technique.PrerequisitesTerraformCode == nil {
		return map[string]string{}, nil
	}

	err := m.StateManager.ExtractTechnique()
	if err != nil {
		return nil, errors.New("unable to extract Terraform file: " + err.Error())
	}

	// We don't want to warm up the technique
	var willWarmUp = m.ShouldWarmUp

	// Technique is already warm
	if m.TechniqueState == stratus.AttackTechniqueStatusWarm && !m.ShouldForce {
		log.Println("Not warming up - " + m.Technique.ID + " is already warm. Use --force to force")
		willWarmUp = false
	}

	if m.TechniqueState == stratus.AttackTechniqueStatusDetonated {
		log.Println(m.Technique.ID + " has been detonated but not cleaned up, not warming up as it should be warm already.")
		willWarmUp = false
	}

	if !willWarmUp {
		outputs, err := m.StateManager.GetTerraformOutputs()
		return outputs, err
	}

	log.Println("Warming up " + m.Technique.ID)
	outputs, err := m.TerraformManager.TerraformInitAndApply(m.TerraformDir)
	if err != nil {
		return nil, errors.New("Unable to run terraform apply on pre-requisite: " + err.Error())
	}

	// Persist outputs to disk
	err = m.StateManager.WriteTerraformOutputs(outputs)
	m.setState(stratus.AttackTechniqueStatusWarm)

	if display, ok := outputs["display"]; ok {
		log.Println(display)
	}
	return outputs, err
}

func (m *Runner) Detonate() error {
	outputs, err := m.WarmUp()
	if err != nil {
		return err
	}
	// Detonate
	err = m.Technique.Detonate(outputs)
	if m.ShouldCleanup {
		defer func() {
			err := m.CleanUp()
			if err != nil {
				log.Println("unable to clean up pre-requisites: " + err.Error())
			}
		}()
	}
	if err != nil {
		return errors.New("Error while detonating attack technique " + m.Technique.ID + ": " + err.Error())
	}
	m.setState(stratus.AttackTechniqueStatusDetonated)
	return nil
}

func (m *Runner) CleanUp() error {
	var techniqueCleanupErr error
	var prerequisitesCleanupErr error

	// Has the technique already been cleaned up?
	if m.TechniqueState == stratus.AttackTechniqueStatusCold && !m.ShouldForce {
		return errors.New(m.Technique.ID + " is already COLD and should already be clean, use --force to force cleanup")
	}

	log.Println("Cleaning up " + m.Technique.ID)

	// Revert detonation
	if m.Technique.Cleanup != nil {
		techniqueCleanupErr = m.Technique.Cleanup()
		if techniqueCleanupErr != nil {
			log.Println("Warning: unable to clean up TTP: " + techniqueCleanupErr.Error())
		}
	}

	// Nuke pre-requisites
	if m.Technique.PrerequisitesTerraformCode != nil {
		log.Println("Cleaning up with terraform destroy")
		prerequisitesCleanupErr = m.TerraformManager.TerraformDestroy(m.TerraformDir)
		if prerequisitesCleanupErr != nil {
			log.Println("Warning: unable to cleanup TTP pre-requisites: " + prerequisitesCleanupErr.Error())
		}
	}

	m.setState(stratus.AttackTechniqueStatusCold)

	// Remove terraform directory
	err := m.StateManager.CleanupTechnique()
	if err != nil {
		log.Println("Warning: unable to remove technique directory " + m.TerraformDir + ": " + err.Error())
	}

	return utils.CoalesceErr(techniqueCleanupErr, prerequisitesCleanupErr, err)
}

func (m *Runner) ValidatePlatformRequirements() {
	switch m.Technique.Platform {
	case stratus.AWS:
		log.Println("Checking your authentication against the AWS API")
		if !providers.AWS().IsAuthenticatedAgainstAWS() {
			log.Fatal("You are not authenticated against AWS, or you have not set your region.")
		}
	}
}

func (m *Runner) GetState() stratus.AttackTechniqueState {
	return m.TechniqueState
}

func (m *Runner) setState(state stratus.AttackTechniqueState) {
	err := m.StateManager.SetTechniqueState(state)
	if err != nil {
		log.Println("Warning: unable to set technique state: " + err.Error())
	}
	m.TechniqueState = state
}