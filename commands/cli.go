package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/gruntwork-io/gruntwork-cli/collections"

	goerrors "errors"
	"github.com/fatih/color"
	"github.com/gruntwork-io/cloud-nuke/aws"
	"github.com/gruntwork-io/cloud-nuke/gcp"
	"github.com/gruntwork-io/cloud-nuke/logging"
	"github.com/gruntwork-io/gruntwork-cli/errors"
	"github.com/gruntwork-io/gruntwork-cli/shell"
	"github.com/urfave/cli"
)

// CreateCli - Create the CLI app with all commands, flags, and usage text configured.
func CreateCli(version string) *cli.App {
	app := cli.NewApp()

	app.Name = "cloud-nuke"
	app.HelpName = app.Name
	app.Author = "Gruntwork <www.gruntwork.io>"
	app.Version = version
	app.Usage = "A CLI tool to cleanup cloud resources (AWS, Azure, GCP). THIS TOOL WILL COMPLETELY REMOVE ALL RESOURCES AND ITS EFFECTS ARE IRREVERSIBLE!!!"
	app.Commands = []cli.Command{
		{
			Name:   "aws",
			Usage:  "Clean up AWS resources (ASG, ELB, ELBv2, EBS, EC2, AMI, Snapshots, ELastic IP)",
			Action: errors.WithPanicHandling(awsNuke),
			Flags: []cli.Flag{
				cli.StringSliceFlag{
					Name:  "exclude-region",
					Usage: "regions to exclude",
				},
				cli.StringFlag{
					Name:  "older-than",
					Usage: "Only delete resources older than this specified value. Can be any valid Go duration, such as 10m or 8h.",
					Value: "0s",
				},
				cli.BoolFlag{
					Name:  "force",
					Usage: "Skip nuke confirmation prompt. WARNING: this will automatically delete all resources without any confirmation",
				},
			},
		},
		{
			Name:   "gcp",
			Usage:  "Clean up GCP resources (GCE instances)",
			Action: errors.WithPanicHandling(gcpNuke),
			Flags: []cli.Flag{
				cli.StringSliceFlag{
					Name:  "exclude-region",
					Usage: "regions to exclude",
				},
				cli.StringFlag{
					Name:  "older-than",
					Usage: "Only delete resources older than this specified value. Can be any valid Go duration, such as 10m or 8h.",
					Value: "0s",
				},
				cli.BoolFlag{
					Name:  "force",
					Usage: "Skip nuke confirmation prompt. WARNING: this will automatically delete all resources without any confirmation",
				},
			},
		},
	}

	return app
}

func parseDurationParam(paramValue string) (*time.Time, error) {
	duration, err := time.ParseDuration(paramValue)
	if err != nil {
		return nil, errors.WithStackTrace(err)
	}

	// make it negative so it goes back in time
	duration = -1 * duration

	excludeAfter := time.Now().Add(duration)
	return &excludeAfter, nil
}

func promptForConfirmationBeforeNuking(force bool) (bool, error) {
	if force {
		logging.Logger.Infoln("The --force flag is set, so waiting for 10 seconds before proceeding to nuke everything in your project. If you don't want to proceed, hit CTRL+C now!!")
		for i := 10; i > 0; i-- {
			fmt.Printf("%d...", i)
			time.Sleep(1 * time.Second)
		}

		fmt.Println()
		return true, nil
	} else {
		color := color.New(color.FgHiRed, color.Bold)
		color.Println("\nTHE NEXT STEPS ARE DESTRUCTIVE AND COMPLETELY IRREVERSIBLE, PROCEED WITH CAUTION!!!")

		prompt := "\nAre you sure you want to nuke all listed resources? Enter 'nuke' to confirm: "
		shellOptions := shell.ShellOptions{Logger: logging.Logger}
		input, err := shell.PromptUserForInput(prompt, &shellOptions)

		if err != nil {
			return false, errors.WithStackTrace(err)
		}

		return strings.ToLower(input) == "nuke", nil
	}
}

func regionIsValid(ctx *gcp.GcpContext, region string) bool {
	return ctx.ContainsRegion(region)
}

func gcpNuke(c *cli.Context) error {
	// TODO accept multiple credentials and nuke resources on all the projects
	// specified by a command line parameter we have authorization for.
	ctx, err := gcp.DefaultContext()
	if err != nil {
		return errors.WithStackTrace(err)
	}

	logging.Logger.Infof("Using project: %s", ctx.Project)

	excludedRegions := c.StringSlice("exclude-region")

	for _, excludedRegion := range excludedRegions {
		if !regionIsValid(ctx, excludedRegion) {
			return InvalidFlagError{
				Name:  "exclude-region",
				Value: excludedRegion,
			}
		}
	}

	excludeAfter, err := parseDurationParam(c.String("older-than"))
	if err != nil {
		return errors.WithStackTrace(err)
	}

	logging.Logger.Infoln("Retrieving all active GCP resources")

	resources, err := ctx.GetAllResources(excludedRegions, *excludeAfter)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	if len(resources) == 0 {
		logging.Logger.Infoln("Nothing to nuke, you're all good!")
		return nil
	}

	logging.Logger.Infoln("The following GCP resources are going to be nuked: ")

	for _, resource := range resources {
		logging.Logger.Infof("* %s: %s Region=%s Zone=%s",
			resource.Kind(), resource.Name(), resource.Region(), resource.Zone())
	}

	confirmation, err := promptForConfirmationBeforeNuking(c.Bool("force"))
	if err != nil {
		return err
	}

	if confirmation {
		nukeErrors := ctx.NukeAllResources(resources)
		if len(nukeErrors) != 0 {
			for _, err := range nukeErrors {
				logging.Logger.Errorf(errors.WithStackTrace(err).Error())
			}
			return goerrors.New("Some resources failed to nuke.")
		}
	}

	return nil
}

func awsNuke(c *cli.Context) error {
	regions := aws.GetAllRegions()
	excludedRegions := c.StringSlice("exclude-region")

	for _, excludedRegion := range excludedRegions {
		if !collections.ListContainsElement(regions, excludedRegion) {
			return InvalidFlagError{
				Name:  "exclude-regions",
				Value: excludedRegion,
			}
		}
	}

	excludeAfter, err := parseDurationParam(c.String("older-than"))
	if err != nil {
		return errors.WithStackTrace(err)
	}

	logging.Logger.Infoln("Retrieving all active AWS resources")
	account, err := aws.GetAllResources(regions, excludedRegions, *excludeAfter)

	if err != nil {
		return errors.WithStackTrace(err)
	}

	if len(account.Resources) == 0 {
		logging.Logger.Infoln("Nothing to nuke, you're all good!")
		return nil
	}

	logging.Logger.Infoln("The following AWS resources are going to be nuked: ")

	for region, resourcesInRegion := range account.Resources {
		for _, resources := range resourcesInRegion.Resources {
			for _, identifier := range resources.ResourceIdentifiers() {
				logging.Logger.Infof("* %s-%s-%s\n", resources.ResourceName(), identifier, region)
			}
		}
	}

	confirmation, err := promptForConfirmationBeforeNuking(c.Bool("force"))
	if err != nil {
		return err
	}

	if confirmation {
		if err := aws.NukeAllResources(account, regions); err != nil {
			return err
		}
	}

	return nil
}
