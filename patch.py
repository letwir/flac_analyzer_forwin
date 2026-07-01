import re

def main():
    with open("a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/main.go", "r", encoding="utf-8") as f:
        content = f.read()

    # 1. Add fields to TaskPayload
    content = content.replace(
        "type TaskPayload struct {\n\tFlacPath     string `json:\"flacPath\"`\n\tFileSize     int64  `json:\"fileSize\"`\n\tTargetScript string `json:\"targetScript\"`\n}",
        "type TaskPayload struct {\n\tFlacPath     string `json:\"flacPath\"`\n\tFileSize     int64  `json:\"fileSize\"`\n\tTargetScript string `json:\"targetScript\"`\n\tTrackNumber  int    `json:\"trackNumber\"`\n\tStartSample  int64  `json:\"startSample\"`\n\tEndSample    int64  `json:\"endSample\"`\n\tTitle        string `json:\"title\"`\n\tArtist       string `json:\"artist\"`\n}"
    )

    # 2. Add TrackSlice struct
    content = content.replace(
        "type TaskPayload struct",
        "type TrackSlice struct {\n\tTrackNumber int    `json:\"track_number\"`\n\tStartSample int64  `json:\"start_sample\"`\n\tEndSample   int64  `json:\"end_sample\"`\n\tTitle       string `json:\"title\"`\n\tArtist      string `json:\"artist\"`\n}\n\ntype TaskPayload struct"
    )

    # 3. Modify computeMD5 to hash track
    content = content.replace(
        "func computeMD5(filePath string) (string, error) {",
        "func computeMD5(filePath string, trackNum int) (string, error) {"
    )
    content = content.replace(
        "return hex.EncodeToString(hash.Sum(nil)), nil",
        "fileHash := hex.EncodeToString(hash.Sum(nil))\n\treturn fmt.Sprintf(\"%s_%02d\", fileHash, trackNum), nil"
    )

    # 4. Update worker MD5 call
    content = content.replace(
        "trackHash, err := computeMD5(task.FlacPath)",
        "trackHash, err := computeMD5(task.FlacPath, task.TrackNumber)"
    )

    # 5. Update cmdDemucs with start/end samples
    content = content.replace(
        "cmdDemucs := exec.Command(pythonPath, \"demucs_worker.py\", \"--flac-path\", task.FlacPath, \"--shm-tags\", string(tagsJson), \"--track-hash\", trackHash)",
        "cmdDemucs := exec.Command(pythonPath, \"demucs_worker.py\", \"--flac-path\", task.FlacPath, \"--shm-tags\", string(tagsJson), \"--track-hash\", trackHash, \"--start-sample\", fmt.Sprintf(\"%d\", task.StartSample), \"--end-sample\", fmt.Sprintf(\"%d\", task.EndSample))"
    )

    # 6. Add Essentia worker after Librosa worker
    librosa_success = """		if errLibrosa := cmdLibrosa.Wait(); errLibrosa != nil {
			log.Printf("%s[W-%d] [IO Monad] Librosa processing failed: %v%s\\n", ColorRed, id, errLibrosa, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		log.Printf("%s[W-%d] [IO Monad] Successfully processed entire pipeline: %s%s\\n", ColorGreen, id, task.FlacPath, ColorReset)"""
    
    essentia_block = """		if errLibrosa := cmdLibrosa.Wait(); errLibrosa != nil {
			log.Printf("%s[W-%d] [IO Monad] Librosa processing failed: %v%s\\n", ColorRed, id, errLibrosa, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		// 5.5 Run Essentia Worker
		log.Printf("%s[W-%d] [IO Monad] Running Essentia worker...%s\\n", ColorPurple, id, ColorReset)
		
		cmdEssentia := exec.Command(pythonPath, "essentia_worker.py", "--shm-metadata", demucsMetaJson, "--track-hash", trackHash)
		cmdEssentia.Dir = parentDir
		cmdEssentia.Env = append(os.Environ(), envVars...)
		
		var essOutBuf bytes.Buffer
		cmdEssentia.Stdout = &essOutBuf
		stderrEssentia, _ := cmdEssentia.StderrPipe()
		
		if err := cmdEssentia.Start(); err != nil {
			log.Printf("%s[W-%d] [IO Monad] Essentia start failed: %v%s\\n", ColorRed, id, err, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		streamColoredLog(stderrEssentia, id, "Essentia", ColorBlue)
		
		if errEssentia := cmdEssentia.Wait(); errEssentia != nil {
			log.Printf("%s[W-%d] [IO Monad] Essentia processing failed: %v%s\\n", ColorRed, id, errEssentia, ColorReset)
			for _, shm := range shmMap {
				shm.Close()
			}
			continue
		}
		
		log.Printf("%s[W-%d] [IO Monad] Successfully processed entire pipeline: %s (Track %d)%s\\n", ColorGreen, id, task.FlacPath, task.TrackNumber, ColorReset)"""
    
    content = content.replace(librosa_success, essentia_block)

    # 7. Update output paths and ingester cmd
    ingester_old = """		// 6. Output Handling
		baseName := filepath.Base(task.FlacPath)
		outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
		
		queueDir := globalConfig.Orchestrator.QueueDir
		if queueDir == "" {
			queueDir = filepath.Join("..", "queue")
		}
		outPath := filepath.Join(queueDir, outName)
		
		// Create queue dir if it doesn't exist
		os.MkdirAll(queueDir, 0755)
		
		if err := os.WriteFile(outPath, libOutBuf.Bytes(), 0644); err != nil {
			log.Printf("[Worker %d] Failed to write local JSON: %v\\n", id, err)
		} else {
			log.Printf("[Worker %d] Saved local JSON to: %s\\n", id, outPath)
			
			// 6.5 Spawn ingester.py asynchronously
			ingesterCmd := exec.Command(pythonPath, "../ingester.py",
				"--flac-path", task.FlacPath,
				"--json-path", outPath,
				"--track-hash", trackHash,
			)"""
            
    ingester_new = """		// 6. Output Handling
		baseName := filepath.Base(task.FlacPath)
		outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
		outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
		
		queueDir := globalConfig.Orchestrator.QueueDir
		if queueDir == "" {
			queueDir = filepath.Join("..", "queue")
		}
		outPath := filepath.Join(queueDir, outName)
		outPathEss := filepath.Join(queueDir, outNameEss)
		
		// Create queue dir if it doesn't exist
		os.MkdirAll(queueDir, 0755)
		
		os.WriteFile(outPathEss, essOutBuf.Bytes(), 0644)
		if err := os.WriteFile(outPath, libOutBuf.Bytes(), 0644); err != nil {
			log.Printf("[Worker %d] Failed to write local JSON: %v\\n", id, err)
		} else {
			log.Printf("[Worker %d] Saved local JSON to: %s\\n", id, outPath)
			
			// 6.5 Spawn ingester.py asynchronously
			ingesterCmd := exec.Command(pythonPath, "../ingester.py",
				"--flac-path", task.FlacPath,
				"--json-path", outPath,
				"--predictions-json-path", outPathEss,
				"--track-hash", trackHash,
				"--track-number", fmt.Sprintf("%d", task.TrackNumber),
				"--title", task.Title,
				"--artist", task.Artist,
			)"""
            
    content = content.replace(ingester_old, ingester_new)

    # 8. Modify HTTP Handler to call extract_cue.py
    handler_old = """		if payload.FlacPath == "" {
			http.Error(w, "flacPath is required", http.StatusBadRequest)
			return
		}

		// Enqueue task
		taskQueue <- payload
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Task accepted: %s\\n", payload.FlacPath)"""

    handler_new = """		if payload.FlacPath == "" {
			http.Error(w, "flacPath is required", http.StatusBadRequest)
			return
		}

		// Call extract_cue.py to get tracks
		exePath, _ := os.Executable()
		parentDir := filepath.Dir(filepath.Dir(exePath))
		cueCmd := exec.Command("python.exe", "extract_cue.py", payload.FlacPath)
		cueCmd.Dir = parentDir
		
		var envVars []string
		for k, v := range globalConfig.PythonEnv {
			envVars = append(envVars, fmt.Sprintf("%s=%s", strings.ToUpper(k), v))
		}
		cueCmd.Env = append(os.Environ(), envVars...)
		
		cueOut, err := cueCmd.Output()
		if err != nil {
			log.Printf("Failed to extract CUE for %s: %v", payload.FlacPath, err)
			// fallback: enqueue as one huge track
			payload.EndSample = -1 // flag for entire file
			taskQueue <- payload
		} else {
			var result struct {
				Status       string       `json:"status"`
				Slices       []TrackSlice `json:"slices"`
				TotalSamples int64        `json:"total_samples"`
			}
			if err := json.Unmarshal(cueOut, &result); err == nil && result.Status == "success" {
				for _, slice := range result.Slices {
					t := payload // copy
					t.TrackNumber = slice.TrackNumber
					t.StartSample = slice.StartSample
					t.EndSample = slice.EndSample
					t.Title = slice.Title
					t.Artist = slice.Artist
					taskQueue <- t
				}
			} else {
				// fallback
				payload.EndSample = -1
				taskQueue <- payload
			}
		}

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Task accepted: %s\\n", payload.FlacPath)"""
        
    content = content.replace(handler_old, handler_new)

    with open("a:/Users/letwir/repo/flac_analyzer_forwin/orchestrator/main.go", "w", encoding="utf-8") as f:
        f.write(content)

if __name__ == "__main__":
    main()
