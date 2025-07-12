package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/spf13/cobra"
	"gopkg.in/ini.v1"
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
	awsRegion       string
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

// loadAWSCredentials loads AWS credentials from ~/.aws/credentials
func loadAWSCredentials() (*credentials.Credentials, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %v", err)
	}

	credentialsPath := filepath.Join(homeDir, ".aws", "credentials")

	// Check if credentials file exists
	if _, err := os.Stat(credentialsPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("AWS credentials file not found at %s", credentialsPath)
	}

	cfg, err := ini.Load(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS credentials: %v", err)
	}

	section := cfg.Section("default")
	accessKeyID := section.Key("aws_access_key_id").String()
	secretAccessKey := section.Key("aws_secret_access_key").String()

	if accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("AWS credentials not found in credentials file")
	}

	return credentials.NewStaticCredentials(accessKeyID, secretAccessKey, ""), nil
}

// createEC2Session creates an AWS session with credentials
func createEC2Session() (*session.Session, error) {
	creds, err := loadAWSCredentials()
	if err != nil {
		return nil, err
	}

	region := awsRegion
	if region == "" {
		region = "us-east-1" // Default region
	}

	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %v", err)
	}

	return sess, nil
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

	sess, err := createEC2Session()
	if err != nil {
		return err
	}

	svc := ec2.New(sess)

	// Generate comprehensive user data script with registration token
	userData := generateUserData(registrationToken, repoOwner, repoName, runnerLabels, preRunnerScript, runnerName)

	// Base64 encode the user data
	userDataEncoded := base64.StdEncoding.EncodeToString([]byte(userData))

	runInput := &ec2.RunInstancesInput{
		ImageId:      aws.String(imageID),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		InstanceType: aws.String(instanceType),
		SubnetId:     aws.String(subnetID),
		SecurityGroupIds: []*string{
			aws.String(securityGroupID),
		},
		UserData: aws.String(userDataEncoded),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String("instance"),
				Tags: []*ec2.Tag{
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
	result, err := svc.RunInstances(runInput)
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
		err = svc.WaitUntilInstanceRunning(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{aws.String(instanceID)},
		})
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
	sess, err := createEC2Session()
	if err != nil {
		return err
	}

	svc := ec2.New(sess)

	// First, describe the instance to check if it exists
	describeInput := &ec2.DescribeInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	}

	_, err = svc.DescribeInstances(describeInput)
	if err != nil {
		return fmt.Errorf("failed to find instance %s: %v", instanceID, err)
	}

	// Terminate the instance
	terminateInput := &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(instanceID)},
	}

	result, err := svc.TerminateInstances(terminateInput)
	if err != nil {
		return fmt.Errorf("failed to terminate instance %s: %v", instanceID, err)
	}

	if len(result.TerminatingInstances) > 0 {
		currentState := *result.TerminatingInstances[0].CurrentState.Name

		if outputFormat == "github-actions" {
			fmt.Printf("Termination Status: %s\n", currentState)
		} else {
			fmt.Printf("✅ Instance %s termination initiated!\n", instanceID)
			fmt.Printf("Current State: %s\n", currentState)
		}

		// Wait for instance to be terminated
		if outputFormat != "github-actions" {
			fmt.Printf("⏳ Waiting for instance to be terminated...\n")
		}
		err = svc.WaitUntilInstanceTerminated(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{aws.String(instanceID)},
		})
		if err != nil {
			if outputFormat != "github-actions" {
				fmt.Printf("⚠️  Termination initiated but failed to wait for terminated state: %v\n", err)
			}
		} else {
			if outputFormat != "github-actions" {
				fmt.Printf("🎉 Instance %s has been terminated!\n", instanceID)
			}
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
	createCmd.Flags().StringVar(&awsRegion, "aws-region", "us-east-1", "AWS region")

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
