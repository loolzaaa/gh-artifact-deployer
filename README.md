# GitHub Artifacts deployer
This application allows you to update an application running in a Linux environment. The deployer download and unzip the artifact, which is the result of the GitHub Action CI workflow. After that, the old version of the application is backed up and replaced by a new version. The corresponding service is stopped/restarted in the process.

## Usage
Clone this repo, build updater and create file with personal access token:
```shell
git clone https://github.com/loolzaaa/gh-artifact-deployer.git
cd gh-artifact-deployer
go build -o updater ./...
chmod 700 updater
echo YOUR_PERSONAL_ACCESS_TOKEN > .pat
```
Create/change configuration file ([default](https://github.com/loolzaaa/gh-artifact-deployer/blob/master/updater.json) `updater.json`) for updater with following properties:
- **artifactApi** - GitHub Actions API for artifacts
- **artifactName** - the name of the artifact to be downloaded
- **applicationFileName** - the name of the application to be updated
- **updatedPrefix** - prefix of the file in artifact zip (because of version, etc.)
- **updatedSuffix** - suffix of the file in artifact zip (usually extension)
- **service.enabled** - flag indicating the need to work with the application's systemd service
- **service.name** - name of the application's systemd service

Run updater:
```shell
./updater -c updater.json -t $(< .pat)
```
