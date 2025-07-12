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
		"mkdir actions-runner && cd actions-runner",
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
		"./run.sh &",
		"echo 'Runner started in background'",
		"echo 'GitHub Actions Runner setup completed successfully!'",
	}

	return strings.Join(userDataLines, "\n")
}

// createEC2Instance creates an EC2 instance with the specified parameters
func createEC2Instance(
	githubToken, imageID, instanceType, subnetID, securityGroupID, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName string,
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
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
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
				},
			},
		},
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

// terminateEC2Instance terminates the specified EC2 instance
func terminateEC2Instance(instanceID string) error {
	svc, err := createEC2Client()
	if err != nil {
		return err
	}

	// First, describe the instance to check if it exists
	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	_, err = svc.DescribeInstances(context.TODO(), describeInput)
	if err != nil {
		return fmt.Errorf("failed to find instance %s: %v", instanceID, err)
	}

	// Terminate the instance
	terminateInput := &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := svc.TerminateInstances(context.TODO(), terminateInput)
	if err != nil {
		return fmt.Errorf("failed to terminate instance %s: %v", instanceID, err)
	}

	if len(result.TerminatingInstances) > 0 {
		currentState := string(result.TerminatingInstances[0].CurrentState.Name)

		if outputFormat == "github-actions" {
			fmt.Printf("Termination Status: %s\n", currentState)
		} else {
			fmt.Printf("✅ Instance %s termination initiated!\n", instanceID)
			fmt.Printf("Current State: %s\n", currentState)
		}

		// If currentState is "shutting-down", return. Otherwise, wait until "shutting-down" or "terminated".
		if currentState == "shutting-down" {
			return nil
		}
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
		return terminateEC2Instance(instanceID)
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
