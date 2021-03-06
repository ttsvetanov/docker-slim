package dockerfile

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/cloudimmunity/go-dockerclientx"
)

type imageInst struct {
	instCmd      string
	instComment  string
	instType     string
	instTime     int64
	layerImageID string
	imageName    string
	shortTags    []string
	fullTags     []string
}

func ReverseDockerfileFromHistory(apiClient *docker.Client, imageID string) ([]string, error) {
	//NOTE: comment field is missing (TODO: enhance the lib...)
	imageHistory, err := apiClient.ImageHistory(imageID)
	if err != nil {
		return nil, err
	}

	log.Debugf("\n\nIMAGE HISTORY =>\n%#v\n\n", imageHistory)

	var fatImageDockerInstructions []imageInst

	imageLayerCount := len(imageHistory)
	imageLayerStart := imageLayerCount - 1
	if imageLayerCount > 0 {
		for idx := imageLayerStart; idx >= 0; idx-- {
			nopPrefix := "/bin/sh -c #(nop) "
			execPrefix := "/bin/sh -c "
			rawLine := imageHistory[idx].CreatedBy
			var inst string

			if len(rawLine) == 0 {
				inst = "FROM scratch"
			} else if strings.HasPrefix(rawLine, nopPrefix) {
				inst = strings.TrimPrefix(rawLine, nopPrefix)
			} else if strings.HasPrefix(rawLine, execPrefix) {
				runData := strings.TrimPrefix(rawLine, execPrefix)
				if strings.Contains(runData, "&&") {
					parts := strings.Split(runData, "&&")
					for i := range parts {
						partPrefix := ""
						if i != 0 {
							partPrefix = "\t"
						}
						parts[i] = partPrefix + strings.TrimSpace(parts[i])
					}
					runDataFormatted := strings.Join(parts, " && \\\n")
					inst = "RUN " + runDataFormatted
				} else {
					inst = "RUN " + runData
				}
			} else {
				inst = rawLine
			}

			if strings.HasPrefix(inst, "ENTRYPOINT ") {
				inst = strings.Replace(inst, "&{[", "[", -1)
				inst = strings.Replace(inst, "]}", "]", -1)
			}

			instInfo := imageInst{
				instCmd:      inst,
				instTime:     imageHistory[idx].Created,
				layerImageID: imageHistory[idx].ID,
				instComment:  imageHistory[idx].Comment,
			}

			instType := "intermediate"
			if idx == imageLayerStart {
				instType = "first"
			}

			if len(imageHistory[idx].Tags) > 0 {
				instType = "last"

				if tagInfo := strings.Split(imageHistory[idx].Tags[0], ":"); len(tagInfo) > 1 {
					instInfo.imageName = tagInfo[0]
				}

				instInfo.fullTags = imageHistory[idx].Tags

				for _, fullTag := range instInfo.fullTags {
					if tagInfo := strings.Split(fullTag, ":"); len(tagInfo) > 1 {
						instInfo.shortTags = append(instInfo.shortTags, tagInfo[1])
					}
				}
			}

			instInfo.instType = instType

			fatImageDockerInstructions = append(fatImageDockerInstructions, instInfo)
		}
	}

	var fatImageDockerfileLines []string
	for idx, instInfo := range fatImageDockerInstructions {
		if instInfo.instType == "first" {
			fatImageDockerfileLines = append(fatImageDockerfileLines, "# new image")
		}

		fatImageDockerfileLines = append(fatImageDockerfileLines, instInfo.instCmd)
		if instInfo.instType == "last" {
			commentText := fmt.Sprintf("# end of image: %s (id: %s tags: %s)",
				instInfo.imageName, instInfo.layerImageID, strings.Join(instInfo.shortTags, ","))
			fatImageDockerfileLines = append(fatImageDockerfileLines, commentText)
			fatImageDockerfileLines = append(fatImageDockerfileLines, "")
			if idx < (len(fatImageDockerInstructions) - 1) {
				fatImageDockerfileLines = append(fatImageDockerfileLines, "# new image")
			}
		}

		if instInfo.instComment != "" {
			fatImageDockerfileLines = append(fatImageDockerfileLines, "# "+instInfo.instComment)
		}

		//TODO: use time diff to separate each instruction
	}

	log.Debugf("IMAGE INSTRUCTIONS:")
	for _, iiLine := range fatImageDockerfileLines {
		log.Debug(iiLine)
	}

	return fatImageDockerfileLines, nil

	//TODO: try adding comments in the docker file to see if the comments
	//show up in the 'history' command

	/*
	   NOTE:
	   Usually "MAINTAINER" is the first instruction,
	   so it can be used to detect a base image.
	*/

	/*
	   TODO:
	   need to have a set of signature for common base images
	   long path: need to discover base images dynamically
	   https://imagelayers.io/?images=alpine:3.1,ubuntu:14.04.1&lock=alpine:3.1

	   https://imagelayers.io/
	   https://github.com/CenturyLinkLabs/imagelayers
	   https://github.com/CenturyLinkLabs/imagelayers-graph
	*/
}

func SaveDockerfileData(fatImageDockerfileLocation string, fatImageDockerInstructions []string) error {
	var data bytes.Buffer
	data.WriteString(strings.Join(fatImageDockerInstructions, "\n"))
	return ioutil.WriteFile(fatImageDockerfileLocation, data.Bytes(), 0644)
}

func GenerateFromInfo(location string,
	workingDir string,
	env []string,
	exposedPorts map[docker.Port]struct{},
	entrypoint []string,
	cmd []string) error {

	dockerfileLocation := filepath.Join(location, "Dockerfile")

	var dfData bytes.Buffer
	dfData.WriteString("FROM scratch\n")
	dfData.WriteString("COPY files /\n")

	if workingDir != "" {
		dfData.WriteString("WORKDIR ")
		dfData.WriteString(workingDir)
		dfData.WriteByte('\n')
	}

	if len(env) > 0 {
		for _, envInfo := range env {
			if envParts := strings.Split(envInfo, "="); len(envParts) > 1 {
				dfData.WriteString("ENV ")
				envLine := fmt.Sprintf("%s %s", envParts[0], envParts[1])
				dfData.WriteString(envLine)
				dfData.WriteByte('\n')
			}
		}
	}

	if len(exposedPorts) > 0 {
		for portInfo := range exposedPorts {
			dfData.WriteString("EXPOSE ")
			dfData.WriteString(string(portInfo))
			dfData.WriteByte('\n')
		}
	}

	if len(entrypoint) > 0 {
		var quotedEntryPoint []string
		for idx := range entrypoint {
			quotedEntryPoint = append(quotedEntryPoint, strconv.Quote(entrypoint[idx]))
		}
		/*
			"Entrypoint": [
			            "/bin/sh",
			            "-c",
			            "node /opt/my/service/server.js"
			        ],
		*/
		dfData.WriteString("ENTRYPOINT [")
		dfData.WriteString(strings.Join(quotedEntryPoint, ","))
		dfData.WriteByte(']')
		dfData.WriteByte('\n')
	}

	if len(cmd) > 0 {
		var quotedCmd []string
		for idx := range cmd {
			quotedCmd = append(quotedCmd, strconv.Quote(cmd[idx]))
		}
		dfData.WriteString("CMD [")
		dfData.WriteString(strings.Join(quotedCmd, ","))
		dfData.WriteByte(']')
		dfData.WriteByte('\n')
	}

	return ioutil.WriteFile(dockerfileLocation, dfData.Bytes(), 0644)
}
