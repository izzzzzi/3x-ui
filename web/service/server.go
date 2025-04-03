package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"x-ui/config"
	"x-ui/database"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/util/sys"
	"x-ui/xray"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

type ProcessState string

const (
	Running ProcessState = "running"
	Stop    ProcessState = "stop"
	Error   ProcessState = "error"
)

type Status struct {
	T           time.Time `json:"-"`
	Cpu         float64   `json:"cpu"`
	CpuCores    int       `json:"cpuCores"`
	LogicalPro  int       `json:"logicalPro"`
	CpuSpeedMhz float64   `json:"cpuSpeedMhz"`
	Mem         struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Swap struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"swap"`
	Disk struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"disk"`
	Xray struct {
		State    ProcessState `json:"state"`
		ErrorMsg string       `json:"errorMsg"`
		Version  string       `json:"version"`
	} `json:"xray"`
	Uptime   uint64    `json:"uptime"`
	Loads    []float64 `json:"loads"`
	TcpCount int       `json:"tcpCount"`
	UdpCount int       `json:"udpCount"`
	NetIO    struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`
	PublicIP struct {
		IPv4 string `json:"ipv4"`
		IPv6 string `json:"ipv6"`
	} `json:"publicIP"`
	AppStats struct {
		Threads uint32 `json:"threads"`
		Mem     uint64 `json:"mem"`
		Uptime  uint64 `json:"uptime"`
	} `json:"appStats"`
}

type Release struct {
	TagName string `json:"tag_name"`
}

type ServerService struct {
	xrayService    XrayService
	inboundService InboundService
	cachedIPv4     string
	cachedIPv6     string
}

func getPublicIP(url string) string {
	resp, err := http.Get(url)
	if err != nil {
		return "N/A"
	}
	defer resp.Body.Close()

	ip, err := io.ReadAll(resp.Body)
	if err != nil {
		return "N/A"
	}

	ipString := string(ip)
	if ipString == "" {
		return "N/A"
	}

	return ipString
}

func (s *ServerService) GetStatus(lastStatus *Status) *Status {
	now := time.Now()
	status := &Status{
		T: now,
	}

	// CPU stats
	percents, err := cpu.Percent(0, false)
	if err != nil {
		logger.Warning("get cpu percent failed:", err)
	} else {
		status.Cpu = percents[0]
	}

	status.CpuCores, err = cpu.Counts(false)
	if err != nil {
		logger.Warning("get cpu cores count failed:", err)
	}

	status.LogicalPro = runtime.NumCPU()

	cpuInfos, err := cpu.Info()
	if err != nil {
		logger.Warning("get cpu info failed:", err)
	} else if len(cpuInfos) > 0 {
		status.CpuSpeedMhz = cpuInfos[0].Mhz
	} else {
		logger.Warning("could not find cpu info")
	}

	// Uptime
	upTime, err := host.Uptime()
	if err != nil {
		logger.Warning("get uptime failed:", err)
	} else {
		status.Uptime = upTime
	}

	// Memory stats
	memInfo, err := mem.VirtualMemory()
	if err != nil {
		logger.Warning("get virtual memory failed:", err)
	} else {
		status.Mem.Current = memInfo.Used
		status.Mem.Total = memInfo.Total
	}

	swapInfo, err := mem.SwapMemory()
	if err != nil {
		logger.Warning("get swap memory failed:", err)
	} else {
		status.Swap.Current = swapInfo.Used
		status.Swap.Total = swapInfo.Total
	}

	// Disk stats
	diskInfo, err := disk.Usage("/")
	if err != nil {
		logger.Warning("get disk usage failed:", err)
	} else {
		status.Disk.Current = diskInfo.Used
		status.Disk.Total = diskInfo.Total
	}

	// Load averages
	avgState, err := load.Avg()
	if err != nil {
		logger.Warning("get load avg failed:", err)
	} else {
		status.Loads = []float64{avgState.Load1, avgState.Load5, avgState.Load15}
	}

	// Network stats
	ioStats, err := net.IOCounters(false)
	if err != nil {
		logger.Warning("get io counters failed:", err)
	} else if len(ioStats) > 0 {
		ioStat := ioStats[0]
		status.NetTraffic.Sent = ioStat.BytesSent
		status.NetTraffic.Recv = ioStat.BytesRecv

		if lastStatus != nil {
			duration := now.Sub(lastStatus.T)
			seconds := float64(duration) / float64(time.Second)
			up := uint64(float64(status.NetTraffic.Sent-lastStatus.NetTraffic.Sent) / seconds)
			down := uint64(float64(status.NetTraffic.Recv-lastStatus.NetTraffic.Recv) / seconds)
			status.NetIO.Up = up
			status.NetIO.Down = down
		}
	} else {
		logger.Warning("can not find io counters")
	}

	// TCP/UDP connections
	status.TcpCount, err = sys.GetTCPCount()
	if err != nil {
		logger.Warning("get tcp connections failed:", err)
	}

	status.UdpCount, err = sys.GetUDPCount()
	if err != nil {
		logger.Warning("get udp connections failed:", err)
	}

	// IP fetching with caching
	if s.cachedIPv4 == "" || s.cachedIPv6 == "" {
		s.cachedIPv4 = getPublicIP("https://api.ipify.org")
		s.cachedIPv6 = getPublicIP("https://api6.ipify.org")
	}
	status.PublicIP.IPv4 = s.cachedIPv4
	status.PublicIP.IPv6 = s.cachedIPv6

	// Xray status
	if s.xrayService.IsXrayRunning() {
		status.Xray.State = Running
		status.Xray.ErrorMsg = ""
	} else {
		err := s.xrayService.GetXrayErr()
		if err != nil {
			status.Xray.State = Error
		} else {
			status.Xray.State = Stop
		}
		status.Xray.ErrorMsg = s.xrayService.GetXrayResult()
	}
	status.Xray.Version = s.xrayService.GetXrayVersion()

	// Application stats
	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)
	status.AppStats.Mem = rtm.Sys
	status.AppStats.Threads = uint32(runtime.NumGoroutine())
	if p != nil && p.IsRunning() {
		status.AppStats.Uptime = p.GetUptime()
	} else {
		status.AppStats.Uptime = 0
	}

	return status
}

func (s *ServerService) GetXrayVersions() ([]string, error) {
	const (
		XrayURL    = "https://api.github.com/repos/XTLS/Xray-core/releases"
		bufferSize = 8192
	)

	resp, err := http.Get(XrayURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	buffer := bytes.NewBuffer(make([]byte, bufferSize))
	buffer.Reset()
	if _, err := buffer.ReadFrom(resp.Body); err != nil {
		return nil, err
	}

	var releases []Release
	if err := json.Unmarshal(buffer.Bytes(), &releases); err != nil {
		return nil, err
	}

	var versions []string
	for _, release := range releases {
		tagVersion := strings.TrimPrefix(release.TagName, "v")
		tagParts := strings.Split(tagVersion, ".")
		if len(tagParts) != 3 {
			continue
		}

		major, err1 := strconv.Atoi(tagParts[0])
		minor, err2 := strconv.Atoi(tagParts[1])
		patch, err3 := strconv.Atoi(tagParts[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		if major > 25 || (major == 25 && minor > 3) || (major == 25 && minor == 3 && patch >= 3) {
			versions = append(versions, release.TagName)
		}
	}
	return versions, nil
}

func (s *ServerService) StopXrayService() (string error) {
	err := s.xrayService.StopXray()
	if err != nil {
		logger.Error("stop xray failed:", err)
		return err
	}

	return nil
}

func (s *ServerService) RestartXrayService() (string error) {
	s.xrayService.StopXray()
	defer func() {
		err := s.xrayService.RestartXray(true)
		if err != nil {
			logger.Error("start xray failed:", err)
		}
	}()

	return nil
}

func (s *ServerService) downloadXRay(version string) (string, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch osName {
	case "darwin":
		osName = "macos"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		arch = "arm64-v8a"
	case "armv7":
		arch = "arm32-v7a"
	case "armv6":
		arch = "arm32-v6"
	case "armv5":
		arch = "arm32-v5"
	case "386":
		arch = "32"
	case "s390x":
		arch = "s390x"
	}

	fileName := fmt.Sprintf("Xray-%s-%s.zip", osName, arch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, fileName)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	os.Remove(fileName)
	file, err := os.Create(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}

func (s *ServerService) UpdateXray(version string) error {
	zipFileName, err := s.downloadXRay(version)
	if err != nil {
		return err
	}

	zipFile, err := os.Open(zipFileName)
	if err != nil {
		return err
	}
	defer func() {
		zipFile.Close()
		os.Remove(zipFileName)
	}()

	stat, err := zipFile.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(zipFile, stat.Size())
	if err != nil {
		return err
	}

	s.xrayService.StopXray()
	defer func() {
		err := s.xrayService.RestartXray(true)
		if err != nil {
			logger.Error("start xray failed:", err)
		}
	}()

	copyZipFile := func(zipName string, fileName string) error {
		zipFile, err := reader.Open(zipName)
		if err != nil {
			return err
		}
		os.Remove(fileName)
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR|os.O_TRUNC, fs.ModePerm)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(file, zipFile)
		return err
	}

	err = copyZipFile("xray", xray.GetBinaryPath())
	if err != nil {
		return err
	}

	return nil
}

func (s *ServerService) GetLogs(count string, level string, syslog string) []string {
	c, _ := strconv.Atoi(count)
	var lines []string

	if syslog == "true" {
		cmdArgs := []string{"journalctl", "-u", "x-ui", "--no-pager", "-n", count, "-p", level}
		// Run the command
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			return []string{"Failed to run journalctl command!"}
		}
		lines = strings.Split(out.String(), "\n")
	} else {
		lines = logger.GetLogs(c, level)
	}

	return lines
}

func (s *ServerService) GetConfigJson() (any, error) {
	config, err := s.xrayService.GetXrayConfig()
	if err != nil {
		return nil, err
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}

	var jsonData any
	err = json.Unmarshal(contents, &jsonData)
	if err != nil {
		return nil, err
	}

	return jsonData, nil
}

func (s *ServerService) GetDb() ([]byte, error) {
	dbType := config.GetDBType()

	// Для SQLite - экспортируем файл базы данных
	if dbType == "sqlite" {
		// Update by manually trigger a checkpoint operation
		err := database.Checkpoint()
		if err != nil {
			return nil, err
		}
		// Open the file for reading
		file, err := os.Open(config.GetDBPath())
		if err != nil {
			return nil, err
		}
		defer file.Close()

		// Read the file contents
		fileContents, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}

		return fileContents, nil
	} else if dbType == "postgres" {
		// Для PostgreSQL - создаем дамп с помощью pg_dump
		log.Println("Exporting PostgreSQL database using pg_dump...")
		// Разбиваем DSN для получения параметров подключения
		dsn := config.GetDBDSN()
		parts := make(map[string]string)
		for _, part := range strings.Split(dsn, " ") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				parts[kv[0]] = kv[1]
			}
		}

		// Формируем команду pg_dump с параметрами
		cmd := exec.Command("pg_dump",
			"-h", parts["host"],
			"-U", parts["user"],
			"-d", parts["dbname"],
			"-p", parts["port"],
			"--clean",     // Добавляем команды для очистки перед восстановлением
			"--if-exists") // Не выдавать ошибку при DROP если объекта нет

		// Получаем пароль из DSN или из переменной окружения PGPASSWORD
		if password, ok := parts["password"]; ok {
			cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))
		}

		var out bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			log.Printf("pg_dump error: %v\nStderr: %s", err, stderr.String())
			return nil, fmt.Errorf("pg_dump failed: %v - %s", err, stderr.String())
		}
		log.Println("PostgreSQL database exported successfully.")
		return out.Bytes(), nil
	} else {
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}

func (s *ServerService) ImportDB(file multipart.File) error {
	dbType := config.GetDBType()
	log.Printf("Importing database for type: %s", dbType)

	// Общая часть: сохраняем файл временно
	tempFile, err := os.CreateTemp("", "dbimport-*.tmp")
	if err != nil {
		return common.NewErrorf("Error creating temp file: %v", err)
	}
	tempFilePath := tempFile.Name()
	log.Printf("Saving uploaded file to temporary path: %s", tempFilePath)
	defer os.Remove(tempFilePath) // Удаляем временный файл в конце

	_, err = io.Copy(tempFile, file)
	if err != nil {
		tempFile.Close() // Закрываем перед возвратом ошибки
		return common.NewErrorf("Error saving uploaded file: %v", err)
	}
	err = tempFile.Close() // Закрываем файл после записи
	if err != nil {
		return common.NewErrorf("Error closing temp file: %v", err)
	}

	// Проверяем, является ли файл базой данных SQLite
	checkFile, err := os.Open(tempFilePath)
	if err != nil {
		return common.NewErrorf("Error opening temp file for check: %v", err)
	}
	isSQLite, err := database.IsSQLiteDB(checkFile)
	checkFile.Close() // Закрываем файл после проверки
	if err != nil {
		return common.NewErrorf("Error checking file format: %v", err)
	}

	// Останавливаем Xray перед модификацией БД
	log.Println("Stopping Xray service...")
	s.StopXrayService()

	// В любом случае, перезапускаем Xray в конце
	defer func() {
		log.Println("Restarting Xray service...")
		err := s.RestartXrayService()
		if err != nil {
			log.Printf("ERROR: Failed to restart Xray after import: %v", err)
		}
	}()

	if dbType == "sqlite" {
		// Логика импорта для SQLite
		log.Println("Processing SQLite import...")
		// Проверяем, действительно ли это SQLite
		if !isSQLite {
			return common.NewError("Invalid SQLite database format. Only .db files are supported for SQLite.")
		}

		// Бэкапим текущую БД
		dbPath := config.GetDBPath()
		fallbackPath := dbPath + ".backup"
		_ = os.Remove(fallbackPath) // Удаляем старый бэкап, если есть
		err = os.Rename(dbPath, fallbackPath)
		// Игнорируем ошибку, если файла не было, но логируем серьезные проблемы
		if err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: Failed to backup existing database: %v", err)
		} else if err == nil {
			defer os.Remove(fallbackPath) // Удаляем бэкап в конце, если он был создан
		}

		// Перемещаем временный файл на место основного файла БД
		log.Printf("Replacing database file at %s", dbPath)
		err = copyFile(tempFilePath, dbPath) // Используем копирование вместо Rename для разных ФС
		if err != nil {
			// Пытаемся восстановить бэкап, если не удалось скопировать
			if _, errStat := os.Stat(fallbackPath); errStat == nil {
				_ = copyFile(fallbackPath, dbPath)
			}
			return common.NewErrorf("Error replacing database file: %v", err)
		}

		return nil

	} else if dbType == "postgres" {
		// Логика импорта для PostgreSQL
		log.Println("Processing PostgreSQL import...")

		// Разбираем DSN для получения параметров подключения
		dsn := config.GetDBDSN()
		parts := make(map[string]string)
		for _, part := range strings.Split(dsn, " ") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				parts[kv[0]] = kv[1]
			}
		}

		// Получаем основные параметры
		host := parts["host"]
		user := parts["user"]
		dbname := parts["dbname"]
		port := parts["port"]
		password := parts["password"]

		// Устанавливаем среду с паролем для подключения
		env := append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", password))

		if isSQLite {
			// Миграция SQLite -> PostgreSQL через pgloader
			log.Printf("Detected SQLite file, checking if pgloader is installed...")

			// Проверяем наличие pgloader
			checkCmd := exec.Command("which", "pgloader")
			err = checkCmd.Run()
			if err != nil {
				log.Printf("pgloader not found: %v", err)
				return common.NewError("pgloader is not installed. To migrate from SQLite to PostgreSQL, please install pgloader first. For Alpine: Add pgloader repository and install it manually.")
			}

			log.Printf("Migrating data using pgloader...")

			// Формируем команду pgloader
			cmd := exec.Command("pgloader",
				"--verbose",          // Для детальных логов
				"--with", "truncate", // Опция для очистки таблиц перед загрузкой
				tempFilePath, // Путь к временному файлу SQLite
				fmt.Sprintf("postgresql://%s:%s@%s:%s/%s", user, password, host, port, dbname)) // DSN для PG

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			cmd.Env = env

			err = cmd.Run()
			if err != nil {
				log.Printf("pgloader error: %v\nStderr: %s", err, stderr.String())
				return common.NewErrorf("pgloader migration failed: %v - %s", err, stderr.String())
			}
			log.Println("pgloader migration completed successfully.")

		} else {
			// Восстановление дампа PostgreSQL через psql
			log.Printf("Detected PostgreSQL dump file, restoring using psql...")

			// Используем psql для выполнения дампа
			cmd := exec.Command("psql",
				"-h", host,
				"-U", user,
				"-d", dbname,
				"-p", port,
				"-v", "ON_ERROR_STOP=1") // Остановиться при первой ошибке

			// Открываем временный файл для чтения
			dumpFile, err := os.Open(tempFilePath)
			if err != nil {
				return common.NewErrorf("Error opening temp dump file for psql: %v", err)
			}
			defer dumpFile.Close()

			cmd.Stdin = dumpFile // Передаем содержимое файла в stdin psql
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			cmd.Env = env

			err = cmd.Run()
			if err != nil {
				log.Printf("psql restore error: %v\nStderr: %s", err, stderr.String())
				return common.NewErrorf("psql restore failed: %v - %s", err, stderr.String())
			}
			log.Println("psql restore completed successfully.")
		}

		return nil
	} else {
		return common.NewError("Unsupported database type for import")
	}
}

// Вспомогательная функция для копирования файлов
func copyFile(src, dst string) error {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()
	_, err = io.Copy(destination, source)
	return err
}

func (s *ServerService) GetNewX25519Cert() (any, error) {
	// Run the command
	cmd := exec.Command(xray.GetBinaryPath(), "x25519")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out.String(), "\n")

	privateKeyLine := strings.Split(lines[0], ":")
	publicKeyLine := strings.Split(lines[1], ":")

	privateKey := strings.TrimSpace(privateKeyLine[1])
	publicKey := strings.TrimSpace(publicKeyLine[1])

	keyPair := map[string]any{
		"privateKey": privateKey,
		"publicKey":  publicKey,
	}

	return keyPair, nil
}
