package main

import (
	"github.com/lcoder/lcoder/pkg/extension"
	"github.com/lcoder/lcoder/pkg/tools"
)

// toolExtensionPluginLoader adapts extension.PluginLoader to the interface
// expected by tools.Registry.LoadExtensions, avoiding an import cycle between
// pkg/tools and pkg/extension.
type toolExtensionPluginLoader struct {
	loader *extension.PluginLoader
}

func newToolExtensionPluginLoader(loader *extension.PluginLoader) tools.ToolExtensionPluginLoader {
	return &toolExtensionPluginLoader{loader: loader}
}

func (l *toolExtensionPluginLoader) LoadPlugin(path string, cfg map[string]any) (tools.ToolExtension, error) {
	return l.loader.LoadPath(path, cfg)
}
