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
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/spf13/cobra"
)

// Constants for configuration
const (
	// GitHub Actions runner version
	GitHubRunnerVersion = "2.313.0"

	// Default AWS region
	DefaultAWSRegion = "us-east-1"

	// Default runner labels
	DefaultRunnerLabels = "self-hosted,linux,x64"

	// GitHub API version
	GitHubAPIVersion = "2022-11-28"

	// HTTP client timeout
	HTTPClientTimeout = 30 * time.Second

	// EC2 instance wait timeout
	EC2WaitTimeout = 5 * time.Minute

	// Default pre-runner script
	DefaultPreRunnerScript = `# Default pre-runner script
echo "Starting GitHub Actions Runner setup..."
apt-get update -y
apt-get install -y curl jq git`
)

var (
	githubToken     string
	imageID         string
	instanceType    string
	subnetID        string
	securityGroupID string
	instanceID      string
	repoOwner       string
	repoName        string
	runnerLabels    string
	preRunnerScript string
	runnerName      string
	outputFormat    string
)

// Singleton HTTP client for reuse
var (
	httpClient *http.Client
	httpOnce   sync.Once
)

// Singleton AWS EC2 client for reuse
var (
	ec2Client *ec2.Client
	ec2Once   sync.Once
)

// getHTTPClient returns a singleton HTTP client
func getHTTPClient() *http.Client {
	httpOnce.Do(func() {
		httpClient = &http.Client{
			Timeout: HTTPClientTimeout,
		}
	})
	return httpClient
}

// GitHubRegistrationTokenResponse represents the response from GitHub API
type GitHubRegistrationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// getGitHubRegistrationToken fetches a runner registration token from GitHub API
func getGitHubRegistrationToken(ctx context.Context, githubToken, repoOwner, repoName string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runners/registration-token", repoOwner, repoName)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", GitHubAPIVersion)

	client := getHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResponse GitHubRegistrationTokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
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

// getAWSRegion gets the AWS region from environment variables or returns default
func getAWSRegion() string {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = DefaultAWSRegion
	}
	return region
}

// createEC2Client creates a singleton AWS EC2 client with credentials
func createEC2Client(ctx context.Context) (*ec2.Client, error) {
	var err error
	ec2Once.Do(func() {
		creds, credErr := loadAWSCredentials()
		if credErr != nil {
			err = credErr
			return
		}

		region := getAWSRegion()

		cfg, cfgErr := config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(creds),
		)
		if cfgErr != nil {
			err = fmt.Errorf("failed to load AWS config: %w", cfgErr)
			return
		}

		ec2Client = ec2.NewFromConfig(cfg)
		fmt.Printf("AWS Region: %s\n", region)
	})

	return ec2Client, err
}

// generateUserData creates a comprehensive user data script for GitHub Actions runner
func generateUserData(registrationToken, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName string) string {
	// Use default pre-runner script if none provided
	if preRunnerScript == "" {
		preRunnerScript = DefaultPreRunnerScript
	}

	// Use default labels if none provided
	if runnerLabels == "" {
		runnerLabels = DefaultRunnerLabels
	}

	// Generate default runner name if none provided
	if runnerName == "" {
		runnerName = "$(hostname)-runner"
	}

	// Build user data script efficiently
	var userDataBuilder strings.Builder
	userDataBuilder.WriteString("#!/bin/bash\n")
	userDataBuilder.WriteString("exec > >(tee /var/log/user-data.log|logger -t user-data -s 2>/dev/console) 2>&1\n")
	userDataBuilder.WriteString("echo 'Starting GitHub Actions Runner setup...'\n")
	userDataBuilder.WriteString("mkdir actions-runner && cd actions-runner\n")
	userDataBuilder.WriteString(fmt.Sprintf("echo \"%s\" > pre-runner-script.sh\n",
		strings.ReplaceAll(preRunnerScript, `"`, `\"`)))
	userDataBuilder.WriteString("chmod +x pre-runner-script.sh\n")
	userDataBuilder.WriteString("source pre-runner-script.sh\n")
	userDataBuilder.WriteString("case $(uname -m) in aarch64) ARCH=\"arm64\" ;; amd64|x86_64) ARCH=\"x64\" ;; esac && export RUNNER_ARCH=${ARCH}\n")
	userDataBuilder.WriteString("echo \"Detected architecture: ${RUNNER_ARCH}\"\n")
	userDataBuilder.WriteString(fmt.Sprintf("curl -O -L https://github.com/actions/runner/releases/download/v%s/actions-runner-linux-${RUNNER_ARCH}-%s.tar.gz\n",
		GitHubRunnerVersion, GitHubRunnerVersion))
	userDataBuilder.WriteString(fmt.Sprintf("tar xzf ./actions-runner-linux-${RUNNER_ARCH}-%s.tar.gz\n", GitHubRunnerVersion))
	userDataBuilder.WriteString("export RUNNER_ALLOW_RUNASROOT=1\n")
	userDataBuilder.WriteString(fmt.Sprintf("./config.sh --url https://github.com/%s/%s --token %s --labels %s --name \"%s\" --work _work --replace\n",
		repoOwner, repoName, registrationToken, runnerLabels, runnerName))
	userDataBuilder.WriteString("echo 'Runner configured successfully'\n")
	userDataBuilder.WriteString("./run.sh &\n")
	userDataBuilder.WriteString("echo 'Runner started in background'\n")
	userDataBuilder.WriteString("echo 'GitHub Actions Runner setup completed successfully!'\n")

	return userDataBuilder.String()
}

// createInstanceTags creates standardized EC2 instance tags
func createInstanceTags(repoOwner, repoName, runnerLabels, runnerName string) []types.Tag {
	return []types.Tag{
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
	}
}

// waitForInstanceRunning waits for EC2 instance to be running
func waitForInstanceRunning(ctx context.Context, svc *ec2.Client, instanceID string) error {
	if outputFormat != "github-actions" {
		fmt.Printf("⏳ Waiting for instance to be running...\n")
	}

	waiter := ec2.NewInstanceRunningWaiter(svc)
	err := waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, EC2WaitTimeout)

	if err != nil {
		if outputFormat != "github-actions" {
			fmt.Printf("⚠️  Instance created but failed to wait for running state: %v\n", err)
		}
		return err
	}

	if outputFormat != "github-actions" {
		fmt.Printf("🎉 Instance is now running!\n")
		fmt.Printf("📋 Check the user data log: ssh into the instance and run 'sudo tail -f /var/log/user-data.log'\n")
	}

	return nil
}

// printInstanceDetails prints instance creation details
func printInstanceDetails(instanceID, instanceType, imageID, subnetID, securityGroupID, repoOwner, repoName, runnerLabels, runnerName string) {
	if outputFormat == "github-actions" {
		// GitHub Actions compatible output
		fmt.Printf("Instance ID: %s\n", instanceID)
		fmt.Printf("Runner Name: %s\n", runnerName)
		fmt.Printf("Labels: %s\n", runnerLabels)
	} else {
		// Human-readable output
		fmt.Printf("✅ EC2 instance created successfully!\n")
		fmt.Printf("Instance ID: %s\n", instanceID)
		fmt.Printf("Instance Type: %s\n", instanceType)
		fmt.Printf("Image ID: %s\n", imageID)
		fmt.Printf("Subnet ID: %s\n", subnetID)
		fmt.Printf("Security Group ID: %s\n", securityGroupID)
		fmt.Printf("Repository: %s/%s\n", repoOwner, repoName)
		fmt.Printf("Runner Labels: %s\n", runnerLabels)
		fmt.Printf("Runner Name: %s\n", runnerName)
	}
}

// createEC2Instance creates an EC2 instance with the specified parameters
func createEC2Instance(
	ctx context.Context,
	githubToken, imageID, instanceType, subnetID, securityGroupID, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName string,
) error {
	// First, get the GitHub runner registration token
	if outputFormat != "github-actions" {
		fmt.Printf("🔑 Fetching GitHub runner registration token...\n")
	}

	registrationToken, err := getGitHubRegistrationToken(ctx, githubToken, repoOwner, repoName)
	if err != nil {
		return fmt.Errorf("failed to get GitHub registration token: %w", err)
	}

	svc, err := createEC2Client(ctx)
	if err != nil {
		return fmt.Errorf("failed to create EC2 client: %w", err)
	}

	// Generate comprehensive user data script with registration token
	userData := generateUserData(registrationToken, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName)
	userDataEncoded := base64.StdEncoding.EncodeToString([]byte(userData))

	// Create EC2 instance input
	runInput := &ec2.RunInstancesInput{
		ImageId:          aws.String(imageID),
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		InstanceType:     types.InstanceType(instanceType),
		SubnetId:         aws.String(subnetID),
		SecurityGroupIds: []string{securityGroupID},
		UserData:         aws.String(userDataEncoded),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags:         createInstanceTags(repoOwner, repoName, runnerLabels, runnerName),
			},
		},
	}

	if outputFormat != "github-actions" {
		fmt.Printf("🚀 Launching EC2 instance...\n")
	}

	result, err := svc.RunInstances(ctx, runInput)
	if err != nil {
		return fmt.Errorf("failed to create EC2 instance: %w", err)
	}

	if len(result.Instances) == 0 {
		return fmt.Errorf("no instances were created")
	}

	instanceID := *result.Instances[0].InstanceId
	printInstanceDetails(instanceID, instanceType, imageID, subnetID, securityGroupID, repoOwner, repoName, runnerLabels, runnerName)

	// Wait for instance to be running (with timeout context)
	waitCtx, cancel := context.WithTimeout(ctx, EC2WaitTimeout)
	defer cancel()

	waitForInstanceRunning(waitCtx, svc, instanceID)

	return nil
}

// terminateEC2Instance terminates the specified EC2 instance
func terminateEC2Instance(ctx context.Context, instanceID string) error {
	svc, err := createEC2Client(ctx)
	if err != nil {
		return fmt.Errorf("failed to create EC2 client: %w", err)
	}

	// First, describe the instance to check if it exists
	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	_, err = svc.DescribeInstances(ctx, describeInput)
	if err != nil {
		return fmt.Errorf("failed to find instance %s: %w", instanceID, err)
	}

	// Terminate the instance
	terminateInput := &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := svc.TerminateInstances(ctx, terminateInput)
	if err != nil {
		return fmt.Errorf("failed to terminate instance %s: %w", instanceID, err)
	}

	if len(result.TerminatingInstances) == 0 {
		return fmt.Errorf("no instances were terminated")
	}

	currentState := string(result.TerminatingInstances[0].CurrentState.Name)

	if outputFormat == "github-actions" {
		fmt.Printf("Termination Status: %s\n", currentState)
	} else {
		fmt.Printf("✅ Instance %s termination initiated!\n", instanceID)
		fmt.Printf("Current State: %s\n", currentState)
	}

	return nil
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

		if outputFormat != "github-actions" {
			fmt.Printf("🚀 Creating EC2 instance for GitHub Actions runner...\n")
		}

		ctx := cmd.Context()
		return createEC2Instance(
			ctx,
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

		if outputFormat != "github-actions" {
			fmt.Printf("🛑 Terminating EC2 instance %s...\n", instanceID)
		}

		ctx := cmd.Context()
		return terminateEC2Instance(ctx, instanceID)
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
	createCmd.Flags().StringVar(&runnerLabels, "labels", DefaultRunnerLabels, "Runner labels (comma-separated)")
	createCmd.Flags().
		StringVar(&preRunnerScript, "pre-runner-script", "", "Pre-runner script to execute before runner setup")
	createCmd.Flags().StringVar(&runnerName, "runner-name", "", "Name for the GitHub Actions runner")
	createCmd.Flags().
		StringVar(&outputFormat, "output-format", "", "Output format (github-actions for GitHub Actions compatibility)")

	// Terminate command flags
	terminateCmd.Flags().StringVar(&instanceID, "instance-id", "", "EC2 instance ID to terminate")
	terminateCmd.Flags().
		StringVar(&outputFormat, "output-format", "", "Output format (github-actions for GitHub Actions compatibility)")

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
