package main

import (
	"github.com/lcoder/lcoder/pkg/extension"
	"github.com/lcoder/lcoder/pkg/tools"
)

type toolExtensionPluginLoader struct {
	loader *extension.PluginLoader
}

func newToolExtensionPluginLoader(loader *extension.PluginLoader) tools.ToolExtensionPluginLoader {
	return &toolExtensionPluginLoader{loader: loader}
}

func (l *toolExtensionPluginLoader) LoadPlugin(path string, cfg map[string]any) (tools.ToolExtension, error) {
	return l.loader.LoadPath(path, cfg)
}
