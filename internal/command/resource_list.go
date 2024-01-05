package command

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	"github.com/posener/complete"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	//"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// ResourceListCommand is a Command implementation that takes a Terraform
// module and clones it to the working directory.
type ResourceListCommand struct {
	Meta
}

func (c *ResourceListCommand) Run(args []string) int {
	var flagFromModule, resourcesOutputFile string
	var flagGet,flagUpgrade bool
	var flagPluginPath FlagStringSlice

	args = c.Meta.process(args)
	cmdFlags := c.Meta.extendedFlagSet("resource list")
	cmdFlags.BoolVar(&flagGet, "get", true, "")
	cmdFlags.BoolVar(&flagUpgrade, "upgrade", false, "")
	cmdFlags.StringVar(&resourcesOutputFile, "output-file", "","output managed resources to one file")

	cmdFlags.Usage = func() { c.Ui.Error(c.Help()) }
	if err := cmdFlags.Parse(args); err != nil {
		return 1
	}

	// Copying the state only happens during backend migration, so setting
	// -force-copy implies -migrate-state
	if c.forceInitCopy {
		c.migrateState = true
	}

	var diags tfdiags.Diagnostics

	if len(flagPluginPath) > 0 {
		c.pluginPath = flagPluginPath
	}

	// Validate the arg count and get the working directory
	args = cmdFlags.Args()
	path, err := ModulePath(args)
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	if err := c.storePluginPath(c.pluginPath); err != nil {
		c.Ui.Error(fmt.Sprintf("Error saving -plugin-path values: %s", err))
		return 1
	}

	// This will track whether we outputted anything so that we know whether
	// to output a newline before the success message
	//var header bool

	if flagFromModule != "" {
		src := flagFromModule

		empty, err := configs.IsEmptyDir(path)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("Error validating destination directory: %s", err))
			return 1
		}
		if !empty {
			c.Ui.Error(strings.TrimSpace(errInitCopyNotEmpty))
			return 1
		}

		c.Ui.Output(c.Colorize().Color(fmt.Sprintf(
			"[reset][bold]Copying configuration[reset] from %q...", src,
		)))
		//header = true

		hooks := uiModuleInstallHooks{
			Ui:             c.Ui,
			ShowLocalPaths: false, // since they are in a weird location for init
		}

		initDirFromModuleAbort, initDirFromModuleDiags := c.initDirFromModule(path, src, hooks)
		diags = diags.Append(initDirFromModuleDiags)
		if initDirFromModuleAbort || initDirFromModuleDiags.HasErrors() {
			c.showDiagnostics(diags)
			return 1
		}

		c.Ui.Output("")
	}

	// If our directory is empty, then we're done. We can't get or set up
	// the backend with an empty directory.
	empty, err := configs.IsEmptyDir(path)
	if err != nil {
		diags = diags.Append(fmt.Errorf("Error checking configuration: %s", err))
		c.showDiagnostics(diags)
		return 1
	}
	if empty {
		c.Ui.Output(c.Colorize().Color(strings.TrimSpace(outputInitEmpty)))
		return 0
	}

	// For Terraform v0.12 we introduced a special loading mode where we would
	// use the 0.11-syntax-compatible "earlyconfig" package as a heuristic to
	// identify situations where it was likely that the user was trying to use
	// 0.11-only syntax that the upgrade tool might help with.
	//
	// However, as the language has moved on that is no longer a suitable
	// heuristic in Terraform 0.13 and later: other new additions to the
	// language can cause the main loader to disagree with earlyconfig, which
	// would lead us to give poor advice about how to respond.
	//
	// For that reason, we no longer use a different error message in that
	// situation, but for now we still use both codepaths because some of our
	// initialization functionality remains built around "earlyconfig" and
	// so we need to still load the module via that mechanism anyway until we
	// can do some more invasive refactoring here.
	rootModEarly, earlyConfDiags := c.loadSingleModuleEarly(path)
	// If _only_ the early loader encountered errors then that's unusual
	// (it should generally be a superset of the normal loader) but we'll
	// return those errors anyway since otherwise we'll probably get
	// some weird behavior downstream. Errors from the early loader are
	// generally not as high-quality since it has less context to work with.
	if earlyConfDiags.HasErrors() {
		c.Ui.Error(c.Colorize().Color(strings.TrimSpace(errInitConfigError)))
		// Errors from the early loader are generally not as high-quality since
		// it has less context to work with.

		// TODO: It would be nice to check the version constraints in
		// rootModEarly.RequiredCore and print out a hint if the module is
		// declaring that it's not compatible with this version of Terraform,
		// and that may be what caused earlyconfig to fail.
		diags = diags.Append(earlyConfDiags)
		c.showDiagnostics(diags)
		return 1
	}

	if flagGet {
		modsOutput, modsAbort, modsDiags := c.getModules(path, rootModEarly, flagUpgrade)
		diags = diags.Append(modsDiags)
		if modsAbort || modsDiags.HasErrors() {
			c.showDiagnostics(diags)
			return 1
		}
		if modsOutput {
			//header = true
		}
	}

	// With all of the modules (hopefully) installed, we can now try to load the
	// whole configuration tree.
	config, _ := c.loadConfig(path)
	if resourcesOutputFile != ""{
		if err := c.showManagedResources(config,  resourcesOutputFile); err != nil {
			c.Ui.Error(fmt.Sprintf("Error loading managed resources: %s", err))
			return 1
		}
	}

	return 0
}

func (c *ResourceListCommand) getModules(path string, earlyRoot *tfconfig.Module, upgrade bool) (output bool, abort bool, diags tfdiags.Diagnostics) {
	if len(earlyRoot.ModuleCalls) == 0 {
		// Nothing to do
		return false, false, nil
	}

	if upgrade {
		c.Ui.Output(c.Colorize().Color("[reset][bold]Upgrading modules..."))
	} else {
		c.Ui.Output(c.Colorize().Color("[reset][bold]Initializing modules..."))
	}

	hooks := uiModuleInstallHooks{
		Ui:             c.Ui,
		ShowLocalPaths: true,
	}

	installAbort, installDiags := c.installModules(path, upgrade, hooks)
	diags = diags.Append(installDiags)

	// At this point, installModules may have generated error diags or been
	// aborted by SIGINT. In any case we continue and the manifest as best
	// we can.

	// Since module installer has modified the module manifest on disk, we need
	// to refresh the cache of it in the loader.
	if c.configLoader != nil {
		if err := c.configLoader.RefreshModules(); err != nil {
			// Should never happen
			diags = diags.Append(tfdiags.Sourceless(
				tfdiags.Error,
				"Failed to read module manifest",
				fmt.Sprintf("After installing modules, Terraform could not re-read the manifest of installed modules. This is a bug in Terraform. %s.", err),
			))
		}
	}

	return true, installAbort, diags
}

// backendConfigOverrideBody interprets the raw values of -backend-config
// arguments into a hcl Body that should override the backend settings given
// in the configuration.
//
// If the result is nil then no override needs to be provided.
//
// If the returned diagnostics contains errors then the returned body may be
// incomplete or invalid.
func (c *ResourceListCommand) backendConfigOverrideBody(flags rawFlags, schema *configschema.Block) (hcl.Body, tfdiags.Diagnostics) {
	items := flags.AllItems()
	if len(items) == 0 {
		return nil, nil
	}

	var ret hcl.Body
	var diags tfdiags.Diagnostics
	synthVals := make(map[string]cty.Value)

	mergeBody := func(newBody hcl.Body) {
		if ret == nil {
			ret = newBody
		} else {
			ret = configs.MergeBodies(ret, newBody)
		}
	}
	flushVals := func() {
		if len(synthVals) == 0 {
			return
		}
		newBody := configs.SynthBody("-backend-config=...", synthVals)
		mergeBody(newBody)
		synthVals = make(map[string]cty.Value)
	}

	if len(items) == 1 && items[0].Value == "" {
		// Explicitly remove all -backend-config options.
		// We do this by setting an empty but non-nil ConfigOverrides.
		return configs.SynthBody("-backend-config=''", synthVals), diags
	}

	for _, item := range items {
		eq := strings.Index(item.Value, "=")

		if eq == -1 {
			// The value is interpreted as a filename.
			newBody, fileDiags := c.loadHCLFile(item.Value)
			diags = diags.Append(fileDiags)
			if fileDiags.HasErrors() {
				continue
			}
			// Generate an HCL body schema for the backend block.
			var bodySchema hcl.BodySchema
			for name := range schema.Attributes {
				// We intentionally ignore the `Required` attribute here
				// because backend config override files can be partial. The
				// goal is to make sure we're not loading a file with
				// extraneous attributes or blocks.
				bodySchema.Attributes = append(bodySchema.Attributes, hcl.AttributeSchema{
					Name: name,
				})
			}
			for name, block := range schema.BlockTypes {
				var labelNames []string
				if block.Nesting == configschema.NestingMap {
					labelNames = append(labelNames, "key")
				}
				bodySchema.Blocks = append(bodySchema.Blocks, hcl.BlockHeaderSchema{
					Type:       name,
					LabelNames: labelNames,
				})
			}
			// Verify that the file body matches the expected backend schema.
			_, schemaDiags := newBody.Content(&bodySchema)
			diags = diags.Append(schemaDiags)
			if schemaDiags.HasErrors() {
				continue
			}
			flushVals() // deal with any accumulated individual values first
			mergeBody(newBody)
		} else {
			name := item.Value[:eq]
			rawValue := item.Value[eq+1:]
			attrS := schema.Attributes[name]
			if attrS == nil {
				diags = diags.Append(tfdiags.Sourceless(
					tfdiags.Error,
					"Invalid backend configuration argument",
					fmt.Sprintf("The backend configuration argument %q given on the command line is not expected for the selected backend type.", name),
				))
				continue
			}
			value, valueDiags := configValueFromCLI(item.String(), rawValue, attrS.Type)
			diags = diags.Append(valueDiags)
			if valueDiags.HasErrors() {
				continue
			}
			synthVals[name] = value
		}
	}

	flushVals()

	return ret, diags
}

func (c *ResourceListCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictDirs("")
}

func (c *ResourceListCommand) Help() string {
	helpText := `
Usage: terraform [global options] resource list [options]

  Initialize a new or existing Terraform working directory by creating
  initial files, loading any remote state, downloading modules, etc.

  This is the first command that should be run for any new or existing
  Terraform configuration per machine. This sets up all the local data
  necessary to run Terraform that is typically not committed to version
  control.

  This command is always safe to run multiple times. Though subsequent runs
  may give errors, this command will never delete your configuration or
  state. Even so, if you have important information, please back it up prior
  to running this command, just in case.

Options:

  -get=false              Disable downloading modules for this configuration.


  -upgrade                Install the latest module and provider versions
                          allowed within configured constraints, overriding the
                          default behavior of selecting exactly the version
                          recorded in the dependency lockfile.

  -output-file=<filename> Set a output file to store managed resources.

`
	return strings.TrimSpace(helpText)
}

func (c *ResourceListCommand) Synopsis() string {
	return "Prepare your working directory for other commands"
}


type managedResource struct {
	ModuleName   string `json:"module_name"`
	ResourceType string `json:"resource_type"`
	ResourceName string `json:"resource_name"`
}

func (c *ResourceListCommand) showManagedResources(config *configs.Config, output string) error{
	c.Ui.Output(c.Colorize().Color("\n[reset][bold]Reading managed resources..."))
	managedResources := make([]managedResource, 0)
	for _, moduleConf := range config.AllModules() {
		for key, value := range moduleConf.Module.ManagedResources {
			managed := managedResource{
				ModuleName: moduleConf.Path.String(),
			}
			managed.ResourceType = value.Type
			managed.ResourceName = value.Name
			if managed.ModuleName != "" {
				c.Ui.Output("- " + managed.ModuleName + "." + key)
			}else {
				c.Ui.Output("- " + key)
			}
			if output != ""{
				managedResources = append(managedResources, managed)
			}
		}

	}
	if len(output) > 0 {
		file, _ := json.MarshalIndent(managedResources, "", " ")
		err :=  os.WriteFile(output, file, 0644)
		if err != nil {
			return err
		}
	}
	return nil
}