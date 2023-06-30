package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/schollz/progressbar/v3"
)

type Artifact struct {
	Id                 int    `json:"id"`
	Name               string `json:"name"`
	Url                string `json:"url"`
	ArchiveDownloadUrl string `json:"archive_download_url"`
	Expired            bool   `json:"expired"`
	CreatedAt          string `json:"created_at"`
}

type ArtifactList struct {
	TotalCount int        `json:"total_count"`
	Artifacts  []Artifact `json:"artifacts"`
}

type Config struct {
	ArtifactApi         string `json:"artifactApi"`
	ArtifactName        string `json:"artifactName"`
	ApplicationFileName string `json:"applicationFileName"`
	UpdatedPrefix       string `json:"updatedPrefix"`
	UpdatedSuffix       string `json:"updatedSuffix"`
	Service             struct {
		Enabled bool   `json:"enabled"`
		Name    string `json:"name"`
	}
}

var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var githubApiHeaders = map[string][]string{
	"Accept":               {"application/vnd.github+json"},
	"X-GitHub-Api-Version": {"2022-11-28"},
}

var config Config

func main() {
	fmt.Println("Starting application updater...")

	var tokenFlag = flag.String("t", "", "personal access token")
	var configFile = flag.String("c", "updater.json", "configuration file name")
	var serverMode = flag.Bool("server", false, "server mode")
	var serverPort = flag.Int("p", 7400, "server port")
	flag.Parse()

	fmt.Print("Loading configuration... ")
	configBytes, err := os.ReadFile(*configFile)
	checkError(err, nil)
	err = json.Unmarshal(configBytes, &config)
	checkError(err, nil)
	fmt.Println("Done.")

	var token string
	fmt.Print("Reading personal access token and setting it as Github REST API request header... ")
	if *tokenFlag != "" {
		token = *tokenFlag
	} else {
		patBytes, err := os.ReadFile(".pat")
		checkError(err, nil)
		token = string(patBytes)
	}
	githubApiHeaders["Authorization"] = []string{"Bearer " + token}
	fmt.Println("Done.")

	if *serverMode {
		http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				defer func(w *http.ResponseWriter) {
					if r := recover(); r != nil {
						fmt.Printf("ERROR: %v\n", r)
						(*w).WriteHeader(http.StatusInternalServerError)
						io.WriteString(*w, fmt.Sprintf("ERROR: %v\n", r))
					}
				}(&w)
				updateApplication()
				io.WriteString(w, "OK\n")
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		})
		err = http.ListenAndServe(fmt.Sprintf("localhost:%d", *serverPort), nil)
		checkError(err, nil)
	} else {
		updateApplication()
	}
}

func updateApplication() {
	var artifactList ArtifactList
	fmt.Print("Requesting list of artifacts... ")
	resp := doRequest("GET", config.ArtifactApi, &githubApiHeaders, 200)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	checkError(err, nil)
	err = json.Unmarshal(body, &artifactList)
	checkError(err, nil)
	fmt.Println("Done.")

	var lastArtifact Artifact
	fmt.Print("Filtering artifacts... ")
	var filteredArtifactsByName = make([]Artifact, 0)
	for _, a := range artifactList.Artifacts {
		if a.Name == config.ArtifactName && !a.Expired {
			filteredArtifactsByName = append(filteredArtifactsByName, a)
		}
	}
	if len(filteredArtifactsByName) > 1 {
		lastArtifact = filteredArtifactsByName[0]
		for i := 1; i < len(filteredArtifactsByName); i++ {
			t1, err := time.Parse(time.RFC3339, lastArtifact.CreatedAt)
			checkError(err, nil)
			t2, err := time.Parse(time.RFC3339, filteredArtifactsByName[i].CreatedAt)
			checkError(err, nil)
			if t2.After(t1) {
				lastArtifact = filteredArtifactsByName[i]
			}
		}
	} else if len(filteredArtifactsByName) == 1 {
		lastArtifact = filteredArtifactsByName[0]
	} else {
		panic(fmt.Sprintf("Can't find artifact with name %s or all artifacts expired", config.ArtifactName))
	}
	fmt.Println("Done.")

	var downloadUrl *url.URL
	fmt.Print("Request artifact download url... ")
	resp = doRequest("GET", lastArtifact.ArchiveDownloadUrl, &githubApiHeaders, 302)
	defer resp.Body.Close()
	downloadUrl, err = resp.Location()
	checkError(err, nil)
	fmt.Println("Done.")

	// Downloading artifact
	resp = doRequest("GET", downloadUrl.String(), nil, 200)
	defer resp.Body.Close()
	tempArtifactFile, err := os.CreateTemp("", fmt.Sprintf("%s_*.zip", config.ArtifactName))
	checkError(err, nil)
	tempArtifactFileName := tempArtifactFile.Name()
	defer os.Remove(tempArtifactFileName)
	bar := progressbar.DefaultBytes(resp.ContentLength, "Downloading artifact")
	_, err = io.Copy(io.MultiWriter(tempArtifactFile, bar), resp.Body)
	checkError(err, nil)
	err = tempArtifactFile.Close()
	checkError(err, nil)
	fmt.Println("\nDone.")

	fmt.Print("Backing up old version of application... ")
	oldApp, err := os.ReadFile(config.ApplicationFileName)
	checkError(err, nil)
	err = os.WriteFile(config.ApplicationFileName+".backup", oldApp, 0664)
	checkError(err, nil)
	fmt.Println("Done.")

	if config.Service.Enabled {
		fmt.Print("Stopping application... ")
		runSystemctlCommand("stop", config.Service.Name)
		fmt.Println("Done.")
	}

	fmt.Print("Updating application... ")
	jarFile, err := os.OpenFile(config.ApplicationFileName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0664)
	checkError(err, restoreOldApplication)
	zipArchive, err := zip.OpenReader(tempArtifactFileName)
	checkError(err, restoreOldApplication)
	defer zipArchive.Close()
	found := false
	for _, f := range zipArchive.File {
		if strings.HasPrefix(f.Name, config.UpdatedPrefix) && strings.HasSuffix(f.Name, config.UpdatedSuffix) {
			found = true
			jarArchiveFile, err := f.Open()
			checkError(err, restoreOldApplication)
			_, err = io.Copy(jarFile, jarArchiveFile)
			checkError(err, restoreOldApplication)
			err = jarFile.Close()
			checkError(err, restoreOldApplication)
			jarArchiveFile.Close()
			break
		}
	}
	if !found {
		restoreOldApplication()
		panic("There is no jar files in artifact")
	} else {
		fmt.Println("Done.")
	}

	if config.Service.Enabled {
		fmt.Print("Starting application... ")
		runSystemctlCommand("daemon-reload")
		runSystemctlCommand("start", config.Service.Name)
		runSystemctlCommand("enable", config.Service.Name)
		time.Sleep(4 * time.Second)
		runSystemctlCommand("status", config.Service.Name)
		fmt.Println("Done.")
	}

	fmt.Println("Application successfully updated!")
}

func runSystemctlCommand(args ...string) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			panic(fmt.Sprintf("systemctl finished with non-zero: %v\n", exitErr))
		} else {
			panic(fmt.Sprintf("failed to run systemctl: %v", err))
		}
	}
	fmt.Println(string(out))
}

func restoreOldApplication() {
	fmt.Print("WARNING! Restoring old version of application... ")
	oldApp, err := os.ReadFile(config.ApplicationFileName + ".backup")
	checkError(err, nil)
	err = os.WriteFile(config.ApplicationFileName, oldApp, 0640)
	checkError(err, nil)
	fmt.Println("Done.")
}

func doRequest(method, url string, headers *map[string][]string, expectedStatus int) *http.Response {
	req, err := http.NewRequest(method, url, nil)
	checkError(err, nil)
	if headers != nil {
		for k, v := range *headers {
			for _, h := range v {
				req.Header.Add(k, h)
			}
		}
	}
	resp, err := httpClient.Do(req)
	checkError(err, nil)
	if resp.StatusCode == expectedStatus {
		return resp
	} else {
		panic(fmt.Sprintf("Request [%s %s] answer status error. Expected %d. Actual: %d",
			method, url, expectedStatus, resp.StatusCode))
	}
}

func checkError(err error, callback func()) {
	if err != nil {
		if callback != nil {
			defer callback()
		}
		panic(err)
	}
}
