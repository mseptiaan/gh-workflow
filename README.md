# GitHub Runner EC2 Manager

A command-line tool and GitHub Action to create and terminate EC2 instances for GitHub Actions runners.

## Features

- ✅ Create EC2 instances configured for GitHub Actions runners
- ✅ Terminate existing EC2 instances
- ✅ Read AWS credentials from `~/.aws/credentials`
- ✅ Wait for instance state changes (running/terminated)
- ✅ Automatic tagging of instances
- ✅ Comprehensive user data script with logging
- ✅ Architecture detection (ARM64/x64)
- ✅ Pre-runner script support
- ✅ Custom runner labels
- ✅ Proper GitHub runner configuration
- ✅ Automatic GitHub registration token generation
- ✅ **GitHub Action support with workflow outputs**

## Usage Methods

### 1. GitHub Action (Recommended)

Use this as a GitHub Action in your workflows to dynamically create and manage self-hosted runners.

#### Basic Usage

```yaml
name: 'Self-hosted Runner Example'

on:
  workflow_dispatch:

jobs:
  start-runner:
    runs-on: ubuntu-latest
    outputs:
      label: ${{ steps.start-ec2-runner.outputs.label }}
      ec2-instance-id: ${{ steps.start-ec2-runner.outputs.ec2-instance-id }}
      runner-name: ${{ steps.start-ec2-runner.outputs.runner-name }}
    steps:
      - name: Start EC2 Runner
        id: start-ec2-runner
        uses: mseptiaan/gh-workflow@v1.0.0
        with:
          mode: start
          github-token: ${{ secrets.GITHUB_TOKEN }}
          image-id: ami-0c55b159cbfafe1d0
          instance-type: t3.micro
          subnet-id: subnet-12345678
          security-group: sg-12345678

  run-tests:
    needs: start-runner
    runs-on: ${{ fromJSON(needs.start-runner.outputs.label) }}
    steps:
      - name: Test on Self-hosted Runner
        run: |
          echo "Running on self-hosted runner!"
          echo "Instance ID: ${{ needs.start-runner.outputs.ec2-instance-id }}"

  stop-runner:
    needs: [start-runner, run-tests]
    runs-on: ubuntu-latest
    if: always()
    steps:
      - name: Stop EC2 Runner
        uses: mseptiaan/gh-workflow@v1.0.0
        with:
          mode: stop
          github-token: ${{ secrets.GITHUB_TOKEN }}
          instance-id: ${{ needs.start-runner.outputs.ec2-instance-id }}
```

#### Action Inputs

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `mode` | ✅ | - | Mode: `start` or `stop` |
| `github-token` | ✅ | - | GitHub personal access token |
| `image-id` | ❌ | - | EC2 AMI image ID |
| `instance-type` | ❌ | `t3.micro` | EC2 instance type |
| `subnet-id` | ❌ | - | VPC subnet ID |
| `security-group` | ❌ | - | Security group ID |
| `repo-owner` | ❌ | Auto-detected | GitHub repository owner |
| `repo-name` | ❌ | Auto-detected | GitHub repository name |
| `labels` | ❌ | `self-hosted,linux,x64` | Runner labels (comma-separated) |
| `pre-runner-script` | ❌ | - | Pre-runner script to execute |
| `instance-id` | ❌ | - | EC2 instance ID (for stop mode) |
| `aws-region` | ❌ | `us-east-1` | AWS region |

#### Action Outputs

| Output | Description |
|--------|-------------|
| `label` | Generated unique label for the runner |
| `ec2-instance-id` | EC2 instance ID of the created runner |
| `runner-name` | Name of the GitHub Actions runner |

#### Advanced Example

```yaml
name: 'Advanced Self-hosted Runner'

on:
  workflow_dispatch:
    inputs:
      runner-type:
        description: 'Runner type'
        required: true
        type: choice
        options:
          - small
          - large

jobs:
  start-runner:
    runs-on: ubuntu-latest
    outputs:
      label: ${{ steps.start-ec2-runner.outputs.label }}
      ec2-instance-id: ${{ steps.start-ec2-runner.outputs.ec2-instance-id }}
      runner-name: ${{ steps.start-ec2-runner.outputs.runner-name }}
    steps:
      - name: Start EC2 Runner
        id: start-ec2-runner
        uses: mseptiaan/gh-workflow@v1.0.0
        with:
          mode: start
          github-token: ${{ secrets.GITHUB_TOKEN }}
          image-id: ami-0c55b159cbfafe1d0
          instance-type: ${{ github.event.inputs.runner-type == 'large' && 't3.large' || 't3.micro' }}
          subnet-id: ${{ secrets.SUBNET_ID }}
          security-group: ${{ secrets.SECURITY_GROUP_ID }}
          labels: self-hosted,linux,x64,${{ github.event.inputs.runner-type }}
          pre-runner-script: |
            # Install Docker
            apt-get update -y
            apt-get install -y docker.io
            systemctl start docker
            usermod -aG docker ubuntu
            
            # Install Node.js
            curl -fsSL https://deb.nodesource.com/setup_18.x | sudo -E bash -
            apt-get install -y nodejs
          aws-region: us-west-2

  # Your jobs here that use the self-hosted runner
  build-and-test:
    needs: start-runner
    runs-on: ${{ fromJSON(needs.start-runner.outputs.label) }}
    steps:
      - uses: actions/checkout@v4
      - name: Run tests
        run: |
          echo "Running on ${{ needs.start-runner.outputs.runner-name }}"
          # Your build and test commands here

  stop-runner:
    needs: [start-runner, build-and-test]
    runs-on: ubuntu-latest
    if: always()
    steps:
      - name: Stop EC2 Runner
        uses: mseptiaan/gh-workflow@v1.0.0
        with:
          mode: stop
          github-token: ${{ secrets.GITHUB_TOKEN }}
          instance-id: ${{ needs.start-runner.outputs.ec2-instance-id }}
          aws-region: us-west-2
```

### 2. Command Line Interface

You can also use the tool directly from the command line.

## Prerequisites

1. **AWS Credentials**: Make sure you have AWS credentials configured in `~/.aws/credentials`:
   ```
   [default]
   aws_access_key_id = YOUR_ACCESS_KEY
   aws_secret_access_key = YOUR_SECRET_KEY
   ```

2. **AWS Permissions**: Your AWS user/role needs the following EC2 permissions:
   - `ec2:RunInstances`
   - `ec2:TerminateInstances`
   - `ec2:DescribeInstances`
   - `ec2:CreateTags`

3. **GitHub Personal Access Token**: You'll need a GitHub personal access token with the following permissions:
   - `repo` (if repository is private)
   - `public_repo` (if repository is public)
   - `admin:org` (if repository belongs to an organization)

   The tool will use this token to call the GitHub API to generate a registration token automatically.

## How It Works

1. **GitHub API Integration**: The tool uses your GitHub personal access token to call the GitHub API endpoint `/repos/{owner}/{repo}/actions/runners/registration-token` to get a temporary registration token.

2. **Secure Token Handling**: The registration token (not your personal access token) is embedded in the EC2 user data script, ensuring your personal token is never stored on the instance.

3. **Automatic Registration**: The EC2 instance automatically registers itself as a GitHub Actions runner using the registration token.

## Building

### Quick Build

```bash
# Build the application
go build -o gh-workflow .

# Or build for different platforms
GOOS=linux GOARCH=amd64 go build -o gh-workflow-linux .
GOOS=windows GOARCH=amd64 go build -o gh-workflow-windows.exe .
```

### Multi-Platform Build Script

Use the provided build script to build binaries for all supported platforms:

```bash
# Build for all platforms
./build.sh

# The script will create binaries in the 'build/' directory with names like:
# gh-workflow-linux-amd64
# gh-workflow-linux-arm64
# gh-workflow-darwin-amd64
# gh-workflow-darwin-arm64
# gh-workflow-windows-amd64.exe
# gh-workflow-windows-arm64.exe
```

The build script supports the following platforms:
- Linux (AMD64, ARM64, 386)
- macOS/Darwin (AMD64, ARM64) 
- Windows (AMD64, ARM64, 386)

### Creating GitHub Releases

Use the provided release script to create GitHub releases with pre-built binaries:

```bash
# Create a new release with binaries
./release.sh -v v1.0.0 -t "Release v1.0.0" -n "Initial release" -b

# Create a draft release
./release.sh -v v1.0.1 -b -d

# Upload binaries to an existing release
./release.sh -v v1.0.0 -u

# Build and create a prerelease
./release.sh -v v1.0.0-beta.1 -b -p
```

Release script options:
- `-v, --version VERSION` - Release version (required)
- `-t, --title TITLE` - Release title (optional)
- `-n, --notes NOTES` - Release notes (optional)
- `-d, --draft` - Create as draft release
- `-p, --prerelease` - Create as prerelease
- `-b, --build` - Build binaries before release
- `-u, --upload-only` - Only upload to existing release

**Requirements for release script:**
- [GitHub CLI](https://cli.github.com/) must be installed
- You must be authenticated with GitHub CLI (`gh auth login`)
- Go must be installed for building

## CLI Usage

### Create an EC2 Instance

```bash
./gh-workflow create \
  --github-token YOUR_GITHUB_PERSONAL_ACCESS_TOKEN \
  --image-id ami-0c55b159cbfafe1d0 \
  --instance-type t3.nano \
  --subnet-id subnet-12345678 \
  --security-group sg-12345678 \
  --repo-owner myorg \
  --repo-name myrepo \
  --labels "self-hosted,linux,x64,my-custom-label" \
  --pre-runner-script "apt-get update -y && apt-get install -y docker.io"
```

### Terminate an EC2 Instance

```bash
./gh-workflow terminate --instance-id i-1234567890abcdef0
```

### Help

```bash
# General help
./gh-workflow --help

# Help for specific commands
./gh-workflow create --help
./gh-workflow terminate --help
```

## Command Line Options

### Create Command

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--github-token` | ✅ | - | GitHub personal access token (not registration token) |
| `--image-id` | ✅ | - | EC2 AMI image ID |
| `--instance-type` | ✅ | - | EC2 instance type |
| `--subnet-id` | ✅ | - | VPC subnet ID |
| `--security-group` | ✅ | - | Security group ID |
| `--repo-owner` | ✅ | - | GitHub repository owner |
| `--repo-name` | ✅ | - | GitHub repository name |
| `--labels` | ❌ | `self-hosted,linux,x64` | Runner labels (comma-separated) |
| `--pre-runner-script` | ❌ | Default system update | Pre-runner script to execute |
| `--runner-name` | ❌ | Auto-generated | Name for the GitHub Actions runner |
| `--output-format` | ❌ | - | Output format (`github-actions` for GitHub Actions compatibility) |
| `--aws-region` | ❌ | `us-east-1` | AWS region |

### Terminate Command

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--instance-id` | ✅ | - | EC2 instance ID to terminate |
| `--output-format` | ❌ | - | Output format (`github-actions` for GitHub Actions compatibility) |

## User Data Script Features

The enhanced user data script includes:

1. **Comprehensive Logging**: All output is logged to `/var/log/user-data.log` and console
2. **Architecture Detection**: Automatically detects ARM64 vs x64 architecture
3. **Pre-runner Script**: Executes custom setup commands before runner installation
4. **Latest Runner Version**: Uses GitHub Actions runner v2.313.0
5. **Secure Token Handling**: Uses registration token (not personal access token)
6. **Proper Configuration**: Automatically configures runner with repository URL and registration token
7. **Background Execution**: Runs the GitHub runner in background
8. **Error Handling**: Includes proper error handling and status messages

## Example User Data Script

```bash
#!/bin/bash
exec > >(tee /var/log/user-data.log|logger -t user-data -s 2>/dev/console) 2>&1
echo 'Starting GitHub Actions Runner setup...'
mkdir actions-runner && cd actions-runner
echo "apt-get update -y && apt-get install -y curl jq git" > pre-runner-script.sh
chmod +x pre-runner-script.sh
source pre-runner-script.sh
case $(uname -m) in aarch64) ARCH="arm64" ;; amd64|x86_64) ARCH="x64" ;; esac && export RUNNER_ARCH=${ARCH}
echo "Detected architecture: ${RUNNER_ARCH}"
curl -O -L https://github.com/actions/runner/releases/download/v2.313.0/actions-runner-linux-${RUNNER_ARCH}-2.313.0.tar.gz
tar xzf ./actions-runner-linux-${RUNNER_ARCH}-2.313.0.tar.gz
export RUNNER_ALLOW_RUNASROOT=1
./config.sh --url https://github.com/myorg/myrepo --token REGISTRATION_TOKEN --labels self-hosted,linux,x64 --name "$(hostname)-runner" --work _work --replace
echo 'Runner configured successfully'
./run.sh &
echo 'Runner started in background'
echo 'GitHub Actions Runner setup completed successfully!'
```

## Example Output

### Creating an Instance (CLI)
```
🚀 Creating EC2 instance for GitHub Actions runner...
🔑 Fetching GitHub runner registration token...
✅ Successfully obtained GitHub runner registration token
🕐 Token expires at: 2024-01-15T10:30:00Z
🚀 Launching EC2 instance...
✅ EC2 instance created successfully!
Instance ID: i-0123456789abcdef0
Instance Type: t3.nano
Image ID: ami-0c55b159cbfafe1d0
Subnet ID: subnet-12345678
Security Group ID: sg-12345678
Repository: myorg/myrepo
Runner Labels: self-hosted,linux,x64,my-custom-label
Runner Name: runner-123-1-1641234567
⏳ Waiting for instance to be running...
🎉 Instance is now running!
📋 Check the user data log: ssh into the instance and run 'sudo tail -f /var/log/user-data.log'
```

### GitHub Actions Output
```
Instance ID: i-0123456789abcdef0
Runner Name: runner-123-1-1641234567
Labels: self-hosted,linux,x64,run-123-1
```

### Terminating an Instance
```
🛑 Terminating EC2 instance i-0123456789abcdef0...
✅ Instance i-0123456789abcdef0 termination initiated!
Current State: shutting-down
⏳ Waiting for instance to be terminated...
🎉 Instance i-0123456789abcdef0 has been terminated!
```

## GitHub Token Requirements

The `--github-token` parameter should be a **GitHub Personal Access Token** (not a registration token). The tool will:

1. Use your personal access token to call the GitHub API
2. Generate a temporary registration token specifically for runner registration
3. Embed the registration token (not your personal token) in the EC2 user data script
4. The registration token expires after 1 hour for security

### Creating a GitHub Personal Access Token

1. Go to GitHub → Settings → Developer settings → Personal access tokens
2. Generate a new token with the following scopes:
   - `repo` (for private repositories)
   - `public_repo` (for public repositories)
   - `admin:org` (if the repository belongs to an organization)

## Configuration

### AWS Region
The default AWS region is set to `us-east-1`. You can modify this in the `createEC2Session()` function in `main.go`.

### Pre-runner Script
You can provide a custom pre-runner script that will be executed before the GitHub runner setup. This is useful for installing dependencies or configuring the environment.

Example pre-runner scripts:
```bash
# Install Docker
apt-get update -y && apt-get install -y docker.io && systemctl start docker

# Install Node.js
curl -fsSL https://deb.nodesource.com/setup_18.x | sudo -E bash - && apt-get install -y nodejs

# Install custom tools
wget -O /usr/local/bin/my-tool https://example.com/my-tool && chmod +x /usr/local/bin/my-tool
```

## Tags

All created instances are automatically tagged with:
- `Name`: "GitHub Actions Runner - {owner}/{repo}"
- `Purpose`: "GitHub Actions"
- `Repository`: "{owner}/{repo}"
- `Labels`: "{runner-labels}"
- `RunnerName`: "{runner-name}"

## Monitoring

To monitor the runner setup process:

1. **SSH into the instance**:
   ```bash
   ssh -i your-key.pem ec2-user@instance-ip
   ```

2. **Check the user data log**:
   ```bash
   sudo tail -f /var/log/user-data.log
   ```

3. **Check runner status**:
   ```bash
   cd actions-runner
   ./run.sh --help
   ```

## Security Features

- **Token Separation**: Personal access tokens are never stored on EC2 instances
- **Temporary Registration Tokens**: Registration tokens expire after 1 hour
- **Secure API Communication**: Uses HTTPS for all GitHub API calls
- **Comprehensive Logging**: All activities are logged for audit purposes

## Error Handling

The tool includes comprehensive error handling for:
- Missing AWS credentials
- Invalid instance IDs
- AWS API errors
- Missing required parameters
- GitHub API authentication errors
- Invalid repository access
- Network connectivity issues

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly
5. Submit a pull request

## License

This project is provided as-is for educational and practical purposes. 