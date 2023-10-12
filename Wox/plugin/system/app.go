package system

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/mitchellh/go-homedir"
	"howett.net/plist"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"wox/plugin"
	"wox/util"
)

func init() {
	plugin.AllSystemPlugin = append(plugin.AllSystemPlugin, &AppPlugin{})
}

type AppPlugin struct {
	api  plugin.API
	apps []appInfo
}

type appInfo struct {
	Name string
	Path string
	Icon plugin.WoxImage
}

func (a *AppPlugin) GetMetadata() plugin.Metadata {
	return plugin.Metadata{
		Id:            "ea2b6859-14bc-4c89-9c88-627da7379141",
		Name:          "App",
		Author:        "Wox Launcher",
		Version:       "1.0.0",
		MinWoxVersion: "2.0.0",
		Runtime:       "Nodejs",
		Description:   "Search app installed on your computer",
		Icon:          "",
		Entry:         "",
		TriggerKeywords: []string{
			"*",
		},
		SupportedOS: []string{
			"Windows",
			"Macos",
			"Linux",
		},
	}
}

func (a *AppPlugin) Init(ctx context.Context, initParams plugin.InitParams) {
	a.api = initParams.API

	appCache, cacheErr := a.loadAppCache(ctx)
	if cacheErr == nil {
		a.apps = appCache
	}

	util.Go(ctx, "index apps", func() {
		a.indexApps(util.NewTraceContext())
	})
}

func (a *AppPlugin) Query(ctx context.Context, query plugin.Query) []plugin.QueryResult {
	var results []plugin.QueryResult
	for _, info := range a.apps {
		if util.StringContains(info.Name, query.Search) {
			results = append(results, plugin.QueryResult{
				Id:       uuid.NewString(),
				Title:    info.Name,
				SubTitle: info.Path,
				Icon:     plugin.WoxImage{},
				Actions: []plugin.QueryResultAction{
					{
						Name: "Open",
						Action: func() {
							runErr := exec.Command("open", info.Path).Run()
							if runErr != nil {
								a.api.Log(ctx, fmt.Sprintf("error openning app %s: %s", info.Path, runErr.Error()))
							}
						},
					},
				},
			})
		}
	}

	return results
}

func (a *AppPlugin) indexApps(ctx context.Context) {
	startTimestamp := util.GetSystemTimestamp()
	var apps []appInfo
	if strings.ToLower(runtime.GOOS) == "darwin" {
		apps = a.getMacApps(ctx)
	}

	if len(apps) > 0 {
		a.api.Log(ctx, fmt.Sprintf("indexed %d apps", len(a.apps)))
		a.apps = apps

		var cachePath = a.getAppCachePath()
		cacheContent, marshalErr := json.Marshal(apps)
		if marshalErr != nil {
			a.api.Log(ctx, fmt.Sprintf("error marshalling app cache: %s", marshalErr.Error()))
			return
		}
		writeErr := os.WriteFile(cachePath, cacheContent, 0644)
		if writeErr != nil {
			a.api.Log(ctx, fmt.Sprintf("error writing app cache: %s", writeErr.Error()))
			return
		}
		a.api.Log(ctx, fmt.Sprintf("wrote app cache to %s", cachePath))
	}

	a.api.Log(ctx, fmt.Sprintf("indexed %d apps, cost %d ms", len(a.apps), util.GetSystemTimestamp()-startTimestamp))
}

func (a *AppPlugin) getAppCachePath() string {
	return path.Join(os.TempDir(), "wox-app-cache.json")
}

func (a *AppPlugin) loadAppCache(ctx context.Context) ([]appInfo, error) {
	startTimestamp := util.GetSystemTimestamp()
	a.api.Log(ctx, "start to load app cache")
	var cachePath = a.getAppCachePath()
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		a.api.Log(ctx, "app cache file not found")
		return nil, err
	}

	cacheContent, readErr := os.ReadFile(cachePath)
	if readErr != nil {
		a.api.Log(ctx, fmt.Sprintf("error reading app cache file: %s", readErr.Error()))
		return nil, readErr
	}

	var apps []appInfo
	unmarshalErr := json.Unmarshal(cacheContent, &apps)
	if unmarshalErr != nil {
		a.api.Log(ctx, fmt.Sprintf("error unmarshalling app cache file: %s", unmarshalErr.Error()))
		return nil, unmarshalErr
	}

	a.api.Log(ctx, fmt.Sprintf("loaded %d apps from cache, cost %d ms", len(apps), util.GetSystemTimestamp()-startTimestamp))
	return apps, nil
}

func (a *AppPlugin) getMacApps(ctx context.Context) []appInfo {
	a.api.Log(ctx, "start to get mac apps")

	userHomeApps, _ := homedir.Expand("~/Applications")
	var appDirectories = []string{
		userHomeApps,
		"/Applications",
		"/Applications/Utilities",
		"/System/Applications",
		"/System/Library/PreferencePanes",
	}

	var appDirectoryPaths []string
	for _, appDirectory := range appDirectories {
		// get all .app directories in appDirectory
		appDir, readErr := os.ReadDir(appDirectory)
		if readErr != nil {
			a.api.Log(ctx, fmt.Sprintf("error reading directory %s: %s", appDirectory, readErr.Error()))
			continue
		}

		for _, entry := range appDir {
			if strings.HasSuffix(entry.Name(), ".app") {
				appDirectoryPaths = append(appDirectoryPaths, path.Join(appDirectory, entry.Name()))
			}
		}
	}

	var appInfos []appInfo
	for _, directoryPath := range appDirectoryPaths {
		info, getErr := a.getMacAppInfo(ctx, directoryPath)
		if getErr != nil {
			a.api.Log(ctx, fmt.Sprintf("error getting app info for %s: %s", directoryPath, getErr.Error()))
			continue
		}

		appInfos = append(appInfos, info)
	}

	return appInfos
}

func (a *AppPlugin) getMacAppInfo(ctx context.Context, path string) (appInfo, error) {
	out, err := exec.Command("mdls", "-name", "kMDItemDisplayName", "-raw", path).Output()
	if err != nil {
		msg := fmt.Sprintf("failed to get app name from mdls(%s): %s", path, err.Error())
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			msg = fmt.Sprintf("failed to get app name from mdls(%s): %s", path, exitError.Stderr)
		}
		return appInfo{}, errors.New(msg)
	}

	info := appInfo{
		Name: strings.TrimSpace(string(out)),
		Path: path,
	}
	icon, iconErr := a.getMacAppIcon(ctx, path)
	if iconErr != nil {
		a.api.Log(ctx, iconErr.Error())
	}
	info.Icon = icon

	return info, nil
}

func (a *AppPlugin) getMacAppIcon(ctx context.Context, appPath string) (plugin.WoxImage, error) {
	// md5 iconPath
	iconPathMd5 := fmt.Sprintf("%x", md5.Sum([]byte(appPath)))
	iconCachePath := path.Join(os.TempDir(), fmt.Sprintf("%s.png", iconPathMd5))
	if _, err := os.Stat(iconCachePath); err == nil {
		return plugin.WoxImage{
			ImageType: plugin.WoxImageTypeAbsolutePath,
			ImageData: iconCachePath,
		}, nil
	}

	rawImagePath, iconErr := a.getMacAppIconImagePath(ctx, appPath)
	if iconErr != nil {
		return plugin.WoxImage{}, iconErr
	}

	if strings.HasSuffix(rawImagePath, ".icns") {
		//use sips to convert icns to png
		//sips -s format png /Applications/Calculator.app/Contents/Resources/AppIcon.icns --out /tmp/wox-app-icon.png
		out, openErr := exec.Command("sips", "-s", "format", "png", rawImagePath, "--out", iconCachePath).Output()
		if openErr != nil {
			msg := fmt.Sprintf("failed to convert icns to png: %s", openErr.Error())
			if out != nil {
				msg = fmt.Sprintf("%s, output: %s", msg, string(out))
			}
			return plugin.WoxImage{}, errors.New(msg)
		}
	} else {
		//copy image to cache
		destF, destErr := os.Create(iconCachePath)
		if destErr != nil {
			return plugin.WoxImage{}, fmt.Errorf("can't create cache file: %s", destErr.Error())
		}
		defer destF.Close()

		originF, originErr := os.Open(rawImagePath)
		if originErr != nil {
			return plugin.WoxImage{}, fmt.Errorf("can't open origin image file: %s", originErr.Error())
		}

		if _, err := io.Copy(destF, originF); err != nil {
			return plugin.WoxImage{}, fmt.Errorf("can't copy image to cache: %s", err.Error())
		}
	}

	a.api.Log(ctx, fmt.Sprintf("app icon cache created: %s", iconCachePath))
	return plugin.WoxImage{
		ImageType: plugin.WoxImageTypeAbsolutePath,
		ImageData: iconCachePath,
	}, nil
}

func (a *AppPlugin) getMacAppIconImagePath(ctx context.Context, appPath string) (string, error) {
	iconPath, infoPlistErr := a.parseMacAppIconFromInfoPlist(ctx, appPath)
	if infoPlistErr == nil {
		return iconPath, nil
	}
	a.api.Log(ctx, fmt.Sprintf("get icon from info.plist fail, path=%s, err=%s", appPath, infoPlistErr.Error()))

	//return default icon
	return "/System/Library/CoreServices/CoreTypes.bundle/Contents/Resources/GenericApplicationIcon.icns", nil
}

func (a *AppPlugin) parseMacAppIconFromInfoPlist(ctx context.Context, appPath string) (string, error) {
	plistPath := path.Join(appPath, "Contents", "Info.plist")
	plistFile, openErr := os.Open(plistPath)
	if openErr != nil {
		plistPath = path.Join(appPath, "WrappedBundle", "Info.plist")
		plistFile, openErr = os.Open(plistPath)
		if openErr != nil {
			return "", fmt.Errorf("can't find Info.plist in this app: %s", openErr.Error())
		}
	}
	defer plistFile.Close()

	decoder := plist.NewDecoder(plistFile)
	var plistData map[string]any
	decodeErr := decoder.Decode(&plistData)
	if decodeErr != nil {
		return "", fmt.Errorf("failed to decode Info.plist: %s", decodeErr.Error())
	}
	iconName, exist := plistData["CFBundleIconFile"].(string)
	if exist {
		if !strings.HasSuffix(iconName, ".icns") {
			iconName = iconName + ".icns"
		}
		iconPath := path.Join(appPath, "Contents", "Resources", iconName)
		if _, statErr := os.Stat(iconPath); os.IsNotExist(statErr) {
			return "", fmt.Errorf("icon file not found: %s", iconPath)
		}

		return iconPath, nil
	} else {
		return "", fmt.Errorf("info plist doesnt have CFBundleIconFile property")
	}
}
