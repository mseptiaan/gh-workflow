package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/spf13/cobra"
)

var (
	githubToken        string
	imageID            string
	instanceType       string
	subnetID           string
	securityGroupID    string
	instanceID         string
	repoOwner          string
	repoName           string
	runnerLabels       string
	preRunnerScript    string
	runnerName         string
	outputFormat       string
	instanceMarketType string
	spotMaxPrice       string
	forceTerminate     bool
	terminationTimeout int
)

// GitHubRegistrationTokenResponse represents the response from GitHub API
type GitHubRegistrationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// getGitHubRegistrationToken fetches a runner registration token from GitHub API
func getGitHubRegistrationToken(githubToken, repoOwner, repoName string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runners/registration-token", repoOwner, repoName)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResponse GitHubRegistrationTokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", fmt.Errorf("failed to parse response: %v", err)
	}

	if outputFormat != "github-actions" {
		fmt.Printf("✅ Successfully obtained GitHub runner registration token\n")
		fmt.Printf("🕐 Token expires at: %s\n", tokenResponse.ExpiresAt.Format(time.RFC3339))
	}

	return tokenResponse.Token, nil
}

// loadAWSCredentials loads AWS credentials from environment variables
func loadAWSCredentials() (aws.CredentialsProvider, error) {
	accessKeyID := os.Getenv("AWS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf(
			"AWS credentials not found in environment variables (AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY required)",
		)
	}

	return credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""), nil
}

// createEC2Client creates an AWS EC2 client with credentials
func createEC2Client() (*ec2.Client, error) {
	creds, err := loadAWSCredentials()
	if err != nil {
		return nil, err
	}

	// Check for region from environment variables, default to us-east-1 if not set
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1" // Default region
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	fmt.Println("AWS Region: ", region)

	return ec2.NewFromConfig(cfg), nil
}

// generateUserData creates a comprehensive user data script for GitHub Actions runner
func generateUserData(registrationToken, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName string) string {
	// Default pre-runner script if none provided
	if preRunnerScript == "" {
		preRunnerScript = `# Default pre-runner script
echo "Starting GitHub Actions Runner setup..."
apt-get update -y
apt-get install -y curl jq git`
	}

	// Default labels if none provided
	if runnerLabels == "" {
		runnerLabels = "self-hosted,linux,x64"
	}

	// Default runner name if none provided
	if runnerName == "" {
		runnerName = "$(hostname)-runner"
	}

	userDataLines := []string{
		"#!/bin/bash",
		"exec > >(tee /var/log/user-data.log|logger -t user-data -s 2>/dev/console) 2>&1",
		"echo 'Starting GitHub Actions Runner setup...'",
		"mkdir -p actions-runner && cd actions-runner",
		fmt.Sprintf(`echo "%s" > pre-runner-script.sh`, strings.ReplaceAll(preRunnerScript, `"`, `\"`)),
		"chmod +x pre-runner-script.sh",
		"source pre-runner-script.sh",
		"case $(uname -m) in aarch64) ARCH=\"arm64\" ;; amd64|x86_64) ARCH=\"x64\" ;; esac && export RUNNER_ARCH=${ARCH}",
		"echo \"Detected architecture: ${RUNNER_ARCH}\"",
		"curl -O -L https://github.com/actions/runner/releases/download/v2.313.0/actions-runner-linux-${RUNNER_ARCH}-2.313.0.tar.gz",
		"tar xzf ./actions-runner-linux-${RUNNER_ARCH}-2.313.0.tar.gz",
		"export RUNNER_ALLOW_RUNASROOT=1",
		fmt.Sprintf(
			`./config.sh --url https://github.com/%s/%s --token %s --labels %s --name "%s" --work _work --replace`,
			repoOwner,
			repoName,
			registrationToken,
			runnerLabels,
			runnerName,
		),
		"echo 'Runner configured successfully'",
		"",
		"# Create cleanup script for graceful shutdown",
		"cat > /usr/local/bin/cleanup-runner.sh << 'EOF'",
		"#!/bin/bash",
		"echo 'Starting graceful runner shutdown...'",
		"",
		"# Change to runner directory",
		"cd /actions-runner || exit 1",
		"",
		"# Stop the runner gracefully",
		"if [ -f .runner ]; then",
		"    echo 'Stopping GitHub Actions Runner...'",
		"    ./config.sh remove --token $(cat .runner | grep token | cut -d' ' -f2)",
		"    echo 'Runner removed from GitHub'",
		"fi",
		"",
		"# Kill any remaining runner processes",
		"pkill -f 'Runner.Listener' || true",
		"pkill -f 'Runner.Worker' || true",
		"pkill -f 'run.sh' || true",
		"",
		"echo 'Runner cleanup completed'",
		"EOF",
		"",
		"chmod +x /usr/local/bin/cleanup-runner.sh",
		"",
		"# Create health check script",
		"cat > /usr/local/bin/health-check.sh << 'EOF'",
		"#!/bin/bash",
		"cd /actions-runner",
		"if [ -f .runner ]; then",
		"    echo 'Runner is configured'",
		"    exit 0",
		"else",
		"    echo 'Runner not configured'",
		"    exit 1",
		"fi",
		"EOF",
		"",
		"chmod +x /usr/local/bin/health-check.sh",
		"",
		"# Set up signal handlers for graceful shutdown",
		"cleanup() {",
		"    echo 'Received termination signal, cleaning up...'",
		"    /usr/local/bin/cleanup-runner.sh",
		"    exit 0",
		"}",
		"",
		"trap cleanup SIGTERM SIGINT",
		"",
		"# Start the runner in background with proper process management",
		"echo 'Starting GitHub Actions Runner...'",
		"./run.sh &",
		"RUNNER_PID=$!",
		"echo $RUNNER_PID > /var/run/github-runner.pid",
		"echo 'Runner started with PID: $RUNNER_PID'",
		"",
		"# Wait for runner to start properly",
		"sleep 10",
		"",
		"# Health check",
		"if /usr/local/bin/health-check.sh; then",
		"    echo '✅ GitHub Actions Runner started successfully!'",
		"else",
		"    echo '❌ Failed to start GitHub Actions Runner'",
		"    exit 1",
		"fi",
		"",
		"# Keep the script running to maintain the instance",
		"wait $RUNNER_PID",
	}

	return strings.Join(userDataLines, "\n")
}

// createEC2Instance creates an EC2 instance with the specified parameters
func createEC2Instance(
	githubToken, imageID, instanceType, subnetID, securityGroupID, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName, instanceMarketType, spotMaxPrice string,
) error {
	// First, get the GitHub runner registration token
	if outputFormat != "github-actions" {
		fmt.Printf("🔑 Fetching GitHub runner registration token...\n")
	}
	registrationToken, err := getGitHubRegistrationToken(githubToken, repoOwner, repoName)
	if err != nil {
		return fmt.Errorf("failed to get GitHub registration token: %v", err)
	}

	svc, err := createEC2Client()
	if err != nil {
		return err
	}

	// Generate comprehensive user data script with registration token
	userData := generateUserData(registrationToken, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName)

	// Base64 encode the user data
	userDataEncoded := base64.StdEncoding.EncodeToString([]byte(userData))

	// Configure spot instance parameters if spot type is requested
	var instanceMarketOptions *types.InstanceMarketOptionsRequest
	if instanceMarketType == "spot" {
		if outputFormat != "github-actions" {
			fmt.Printf("🎯 Configuring spot instance...\n")
		}

		spotOptions := &types.SpotMarketOptions{
			SpotInstanceType: types.SpotInstanceTypeOneTime,
		}

		// Set max price if specified
		if spotMaxPrice != "" {
			spotOptions.MaxPrice = aws.String(spotMaxPrice)
			if outputFormat != "github-actions" {
				fmt.Printf("💰 Setting spot max price: $%s/hour\n", spotMaxPrice)
			}
		}

		instanceMarketOptions = &types.InstanceMarketOptionsRequest{
			MarketType:  types.MarketTypeSpot,
			SpotOptions: spotOptions,
		}
	} else {
		if outputFormat != "github-actions" && instanceMarketType == "on-demand" {
			fmt.Printf("🔒 Configuring on-demand instance...\n")
		}
	}

	runInput := &ec2.RunInstancesInput{
		ImageId:      aws.String(imageID),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		InstanceType: types.InstanceType(instanceType),
		SubnetId:     aws.String(subnetID),
		SecurityGroupIds: []string{
			securityGroupID,
		},
		UserData: aws.String(userDataEncoded),
	}

	// Build tags dynamically
	tags := []types.Tag{
		{
			Key:   aws.String("Name"),
			Value: aws.String(fmt.Sprintf("GitHub Actions Runner - %s/%s", repoOwner, repoName)),
		},
		{
			Key:   aws.String("Purpose"),
			Value: aws.String("GitHub Actions"),
		},
		{
			Key:   aws.String("Repository"),
			Value: aws.String(fmt.Sprintf("%s/%s", repoOwner, repoName)),
		},
		{
			Key:   aws.String("Labels"),
			Value: aws.String(runnerLabels),
		},
		{
			Key:   aws.String("RunnerName"),
			Value: aws.String(runnerName),
		},
		{
			Key:   aws.String("InstanceMarketType"),
			Value: aws.String(instanceMarketType),
		},
	}

	// Add spot price tag if specified
	if instanceMarketType == "spot" && spotMaxPrice != "" {
		tags = append(tags, types.Tag{
			Key:   aws.String("SpotMaxPrice"),
			Value: aws.String(spotMaxPrice),
		})
	}

	runInput.TagSpecifications = []types.TagSpecification{
		{
			ResourceType: types.ResourceTypeInstance,
			Tags:         tags,
		},
	}

	// Add spot instance configuration if specified
	if instanceMarketOptions != nil {
		runInput.InstanceMarketOptions = instanceMarketOptions
	}

	if outputFormat != "github-actions" {
		fmt.Printf("🚀 Launching EC2 instance...\n")
	}
	result, err := svc.RunInstances(context.TODO(), runInput)
	if err != nil {
		return fmt.Errorf("failed to create EC2 instance: %v", err)
	}

	if len(result.Instances) > 0 {
		instanceID := *result.Instances[0].InstanceId

		if outputFormat == "github-actions" {
			// GitHub Actions compatible output
			fmt.Printf("Instance ID: %s\n", instanceID)
			fmt.Printf("Runner Name: %s\n", runnerName)
			fmt.Printf("Labels: %s\n", runnerLabels)
			fmt.Printf("Instance Market Type: %s\n", instanceMarketType)
			if instanceMarketType == "spot" && spotMaxPrice != "" {
				fmt.Printf("Spot Max Price: %s\n", spotMaxPrice)
			}
		} else {
			// Human-readable output
			fmt.Printf("✅ EC2 instance created successfully!\n")
			fmt.Printf("Instance ID: %s\n", instanceID)
			fmt.Printf("Instance Type: %s\n", instanceType)
			fmt.Printf("Instance Market Type: %s\n", instanceMarketType)
			if instanceMarketType == "spot" && spotMaxPrice != "" {
				fmt.Printf("Spot Max Price: $%s/hour\n", spotMaxPrice)
			}
			fmt.Printf("Image ID: %s\n", imageID)
			fmt.Printf("Subnet ID: %s\n", subnetID)
			fmt.Printf("Security Group ID: %s\n", securityGroupID)
			fmt.Printf("Repository: %s/%s\n", repoOwner, repoName)
			fmt.Printf("Runner Labels: %s\n", runnerLabels)
			fmt.Printf("Runner Name: %s\n", runnerName)
		}

		// Wait for instance to be running
		if outputFormat != "github-actions" {
			fmt.Printf("⏳ Waiting for instance to be running...\n")
		}
		waiter := ec2.NewInstanceRunningWaiter(svc)
		err = waiter.Wait(context.TODO(), &ec2.DescribeInstancesInput{
			InstanceIds: []string{instanceID},
		}, time.Minute*5)
		if err != nil {
			if outputFormat != "github-actions" {
				fmt.Printf("⚠️  Instance created but failed to wait for running state: %v\n", err)
			}
		} else {
			if outputFormat != "github-actions" {
				fmt.Printf("🎉 Instance is now running!\n")
				fmt.Printf("📋 Check the user data log: ssh into the instance and run 'sudo tail -f /var/log/user-data.log'\n")
			}
		}
	}

	return nil
}

// terminateEC2Instance terminates the specified EC2 instance with improved error handling
func terminateEC2Instance(instanceID string, force bool, timeoutSeconds int) error {
	svc, err := createEC2Client()
	if err != nil {
		return err
	}

	// First, describe the instance to check current state
	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := svc.DescribeInstances(context.TODO(), describeInput)
	if err != nil {
		return fmt.Errorf("failed to find instance %s: %v", instanceID, err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return fmt.Errorf("instance %s not found", instanceID)
	}

	instance := result.Reservations[0].Instances[0]
	currentState := string(instance.State.Name)

	if outputFormat != "github-actions" {
		fmt.Printf("📊 Instance %s current state: %s\n", instanceID, currentState)

		// Check if it's a spot instance (Note: InstanceMarketOptions might not be available in all SDK versions)
	}

	// Check if instance is already terminated
	if currentState == "terminated" {
		if outputFormat == "github-actions" {
			fmt.Printf("Termination Status: %s\n", currentState)
		} else {
			fmt.Printf("ℹ️  Instance %s is already terminated\n", instanceID)
		}
		return nil
	}

	// Check if instance is in a terminable state
	if currentState == "shutting-down" {
		if outputFormat == "github-actions" {
			fmt.Printf("Termination Status: %s\n", currentState)
		} else {
			fmt.Printf("⏳ Instance %s is already shutting down\n", instanceID)
		}
		// Wait for termination to complete
		return waitForInstanceTermination(svc, instanceID, timeoutSeconds)
	}

	// Only attempt termination if instance is in running, stopping, or stopped state
	if currentState != "running" && currentState != "stopping" && currentState != "stopped" {
		return fmt.Errorf("instance %s is in state '%s' and cannot be terminated", instanceID, currentState)
	}

	// Attempt graceful termination first
	if outputFormat != "github-actions" {
		if force {
			fmt.Printf("🛑 Force terminating instance %s...\n", instanceID)
		} else {
			fmt.Printf("🛑 Initiating graceful termination of instance %s...\n", instanceID)
		}
	}

	// For force termination, skip graceful shutdown attempts
	if !force {
		// Try graceful termination with retry logic
		maxRetries := 3
		for attempt := 1; attempt <= maxRetries; attempt++ {
			if outputFormat != "github-actions" && attempt > 1 {
				fmt.Printf("🔄 Retry attempt %d/%d...\n", attempt, maxRetries)
			}

			terminateInput := &ec2.TerminateInstancesInput{
				InstanceIds: []string{instanceID},
			}

			terminateResult, err := svc.TerminateInstances(context.TODO(), terminateInput)
			if err != nil {
				// Check for specific AWS errors
				if strings.Contains(err.Error(), "IncorrectInstanceState") {
					if outputFormat != "github-actions" {
						fmt.Printf("⚠️  Instance is in a state that prevents termination: %s\n", currentState)
						fmt.Printf("💡 Try using --force flag for force termination\n")
					}
					return fmt.Errorf(
						"instance %s is in state '%s' and cannot be terminated gracefully",
						instanceID,
						currentState,
					)
				}

				// For other errors, retry if not the last attempt
				if attempt < maxRetries {
					time.Sleep(time.Duration(attempt) * time.Second)
					continue
				}
				return fmt.Errorf("failed to terminate instance %s after %d attempts: %v", instanceID, maxRetries, err)
			}

			// Success - break out of retry loop
			if len(terminateResult.TerminatingInstances) > 0 {
				newState := string(terminateResult.TerminatingInstances[0].CurrentState.Name)

				if outputFormat == "github-actions" {
					fmt.Printf("Termination Status: %s\n", newState)
				} else {
					fmt.Printf("✅ Instance %s termination initiated!\n", instanceID)
					fmt.Printf("Current State: %s\n", newState)
				}

				// Wait for termination to complete
				return waitForInstanceTermination(svc, instanceID, timeoutSeconds)
			}
		}
	} else {
		// Force termination - try multiple times with different approaches
		if outputFormat != "github-actions" {
			fmt.Printf("🔨 Using force termination methods...\n")
		}

		// Method 1: Standard termination
		terminateInput := &ec2.TerminateInstancesInput{
			InstanceIds: []string{instanceID},
		}

		terminateResult, err := svc.TerminateInstances(context.TODO(), terminateInput)
		if err == nil && len(terminateResult.TerminatingInstances) > 0 {
			newState := string(terminateResult.TerminatingInstances[0].CurrentState.Name)

			if outputFormat == "github-actions" {
				fmt.Printf("Termination Status: %s\n", newState)
			} else {
				fmt.Printf("✅ Force termination initiated!\n")
				fmt.Printf("Current State: %s\n", newState)
			}

			// Wait for termination to complete
			forceTimeout := timeoutSeconds / 2
			if forceTimeout < 120 {
				forceTimeout = 120 // Minimum 2 minutes for force termination
			}
			return waitForInstanceTermination(svc, instanceID, forceTimeout)
		}

		// Method 2: If standard termination fails, try stop + terminate for stubborn instances
		if outputFormat != "github-actions" {
			fmt.Printf("⚡ Standard termination failed, trying stop + terminate...\n")
		}

		stopInput := &ec2.StopInstancesInput{
			InstanceIds: []string{instanceID},
			Force:       aws.Bool(true),
		}

		_, stopErr := svc.StopInstances(context.TODO(), stopInput)
		if stopErr != nil {
			if outputFormat != "github-actions" {
				fmt.Printf("⚠️  Stop also failed: %v\n", stopErr)
			}
		} else {
			if outputFormat != "github-actions" {
				fmt.Printf("⏹️  Instance stopped, now terminating...\n")
			}
			time.Sleep(10 * time.Second) // Wait for stop to complete
		}

		// Try termination again after stop
		terminateResult, err = svc.TerminateInstances(context.TODO(), terminateInput)
		if err != nil {
			return fmt.Errorf("force termination failed for instance %s: %v", instanceID, err)
		}

		if len(terminateResult.TerminatingInstances) > 0 {
			newState := string(terminateResult.TerminatingInstances[0].CurrentState.Name)

			if outputFormat == "github-actions" {
				fmt.Printf("Termination Status: %s\n", newState)
			} else {
				fmt.Printf("✅ Force termination successful!\n")
				fmt.Printf("Current State: %s\n", newState)
			}

			// Wait for termination to complete
			forceTimeout := timeoutSeconds / 2
			if forceTimeout < 120 {
				forceTimeout = 120 // Minimum 2 minutes for force termination
			}
			return waitForInstanceTermination(svc, instanceID, forceTimeout)
		}
	}

	return fmt.Errorf(
		"force termination failed for instance %s: unexpected response from terminate instances API",
		instanceID,
	)
}

// waitForInstanceTermination waits for an instance to fully terminate
func waitForInstanceTermination(svc *ec2.Client, instanceID string, timeoutSeconds int) error {
	if outputFormat != "github-actions" {
		fmt.Printf("⏳ Waiting for instance %s to terminate...\n", instanceID)
	}

	timeout := time.After(time.Duration(timeoutSeconds) * time.Second)
	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf(
				"timeout waiting for instance %s to terminate after %d seconds",
				instanceID,
				timeoutSeconds,
			)

		case <-ticker.C:
			describeInput := &ec2.DescribeInstancesInput{
				InstanceIds: []string{instanceID},
			}

			result, err := svc.DescribeInstances(context.TODO(), describeInput)
			if err != nil {
				// If we can't describe the instance, it might be terminated
				if strings.Contains(err.Error(), "InvalidInstanceId.NotFound") {
					if outputFormat != "github-actions" {
						fmt.Printf("🎉 Instance %s has been terminated!\n", instanceID)
					}
					return nil
				}
				return fmt.Errorf("error checking instance state: %v", err)
			}

			if len(result.Reservations) > 0 && len(result.Reservations[0].Instances) > 0 {
				state := string(result.Reservations[0].Instances[0].State.Name)

				if outputFormat != "github-actions" {
					fmt.Printf("📊 Instance state: %s\n", state)
				}

				if state == "terminated" {
					if outputFormat != "github-actions" {
						fmt.Printf("🎉 Instance %s has been successfully terminated!\n", instanceID)
					}
					return nil
				}

				// Continue waiting if still terminating
				if state == "shutting-down" || state == "stopping" {
					continue
				}

				// Unexpected state
				if state != "running" && state != "pending" && state != "stopping" && state != "stopped" &&
					state != "shutting-down" {
					return fmt.Errorf("instance %s is in unexpected state: %s", instanceID, state)
				}
			}
		}
	}
}

var rootCmd = &cobra.Command{
	Use:   "gh-workflow",
	Short: "A CLI tool to manage GitHub Actions EC2 runners",
	Long:  "A command-line tool to create and terminate EC2 instances for GitHub Actions runners",
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new EC2 instance for GitHub Actions runner",
	Long:  "Create a new EC2 instance configured as a GitHub Actions runner",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate required flags
		if githubToken == "" {
			return fmt.Errorf("github-token is required (GitHub personal access token)")
		}
		if imageID == "" {
			return fmt.Errorf("image-id is required")
		}
		if instanceType == "" {
			return fmt.Errorf("instance-type is required")
		}
		if subnetID == "" {
			return fmt.Errorf("subnet-id is required")
		}
		if securityGroupID == "" {
			return fmt.Errorf("security-group is required")
		}
		if repoOwner == "" {
			return fmt.Errorf("repo-owner is required")
		}
		if repoName == "" {
			return fmt.Errorf("repo-name is required")
		}

		// Validate instance market type
		if instanceMarketType != "on-demand" && instanceMarketType != "spot" {
			return fmt.Errorf("instance-market-type must be 'on-demand' or 'spot'")
		}

		if outputFormat != "github-actions" {
			fmt.Printf("🚀 Creating EC2 instance for GitHub Actions runner...\n")
		}
		return createEC2Instance(
			githubToken,
			imageID,
			instanceType,
			subnetID,
			securityGroupID,
			repoOwner,
			repoName,
			runnerLabels,
			preRunnerScript,
			runnerName,
			instanceMarketType,
			spotMaxPrice,
		)
	},
}

var terminateCmd = &cobra.Command{
	Use:   "terminate",
	Short: "Terminate an existing EC2 instance",
	Long:  "Terminate an existing EC2 instance by its instance ID",
	RunE: func(cmd *cobra.Command, args []string) error {
		if instanceID == "" {
			return fmt.Errorf("instance-id is required")
		}

		// Validate timeout range
		if terminationTimeout < 60 {
			return fmt.Errorf("timeout must be at least 60 seconds")
		}
		if terminationTimeout > 3600 {
			return fmt.Errorf("timeout cannot exceed 3600 seconds (1 hour)")
		}

		if outputFormat != "github-actions" {
			if forceTerminate {
				fmt.Printf("🛑 Force terminating EC2 instance %s (timeout: %ds)...\n", instanceID, terminationTimeout)
			} else {
				fmt.Printf("🛑 Terminating EC2 instance %s (timeout: %ds)...\n", instanceID, terminationTimeout)
			}
		}
		return terminateEC2Instance(instanceID, forceTerminate, terminationTimeout)
	},
}

func init() {
	// Create command flags
	createCmd.Flags().
		StringVar(&githubToken, "github-token", "", "GitHub personal access token (not registration token)")
	createCmd.Flags().StringVar(&imageID, "image-id", "", "EC2 AMI image ID")
	createCmd.Flags().StringVar(&instanceType, "instance-type", "", "EC2 instance type")
	createCmd.Flags().StringVar(&subnetID, "subnet-id", "", "VPC subnet ID")
	createCmd.Flags().StringVar(&securityGroupID, "security-group", "", "Security group ID")
	createCmd.Flags().StringVar(&repoOwner, "repo-owner", "", "GitHub repository owner")
	createCmd.Flags().StringVar(&repoName, "repo-name", "", "GitHub repository name")
	createCmd.Flags().StringVar(&runnerLabels, "labels", "self-hosted,linux,x64", "Runner labels (comma-separated)")
	createCmd.Flags().
		StringVar(&preRunnerScript, "pre-runner-script", "", "Pre-runner script to execute before runner setup")
	createCmd.Flags().StringVar(&runnerName, "runner-name", "", "Name for the GitHub Actions runner")
	createCmd.Flags().
		StringVar(&outputFormat, "output-format", "", "Output format (github-actions for GitHub Actions compatibility)")
	createCmd.Flags().
		StringVar(&instanceMarketType, "instance-market-type", "on-demand", "Instance market type (on-demand or spot)")
	createCmd.Flags().
		StringVar(&spotMaxPrice, "spot-max-price", "", "Maximum price for spot instances (per hour in USD, optional)")

	// Terminate command flags
	terminateCmd.Flags().StringVar(&instanceID, "instance-id", "", "EC2 instance ID to terminate")
	terminateCmd.Flags().
		StringVar(&outputFormat, "output-format", "", "Output format (github-actions for GitHub Actions compatibility)")
	terminateCmd.Flags().BoolVar(&forceTerminate, "force", false, "Force termination even if graceful shutdown fails")
	terminateCmd.Flags().
		IntVar(&terminationTimeout, "timeout", 300, "Maximum time in seconds to wait for termination (60-3600, default: 300)")

	// Add commands to root
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(terminateCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
