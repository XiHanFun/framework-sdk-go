package plugin

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/xihanfun/framework-sdk-go/primitives"
)

// Locales 是内置的 10 种区域（RFC-0010）；en-US 为默认。
var Locales = []string{"en-US", "zh-CN", "zh-TW", "ja-JP", "ko-KR", "de-DE", "es-ES", "fr-FR", "pt-BR", "ru-RU"}

// LoadCatalog 从文件系统的一个目录读取消息目录：每种区域一个文件 <区域>.json，内容是键 → 模板。
// 通常配合 embed.FS 使用：//go:embed Localization 后 LoadCatalog("echo", localization, "Localization")。
func LoadCatalog(name string, fsys fs.FS, directory string) (Catalog, error) {
	entries, err := fs.ReadDir(fsys, directory)
	if err != nil {
		return Catalog{}, fmt.Errorf("plugin: read catalog directory %q: %w", directory, err)
	}
	catalog := Catalog{Name: name, Locales: map[string]map[string]string{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(directory, entry.Name()))
		if err != nil {
			return Catalog{}, fmt.Errorf("plugin: read catalog file %q: %w", entry.Name(), err)
		}
		var templates map[string]string
		if err := json.Unmarshal(data, &templates); err != nil {
			return Catalog{}, fmt.Errorf("plugin: parse catalog file %q: %w", entry.Name(), err)
		}
		for key := range templates {
			if !primitives.IsMessageKey(key) {
				return Catalog{}, fmt.Errorf("plugin: catalog file %q has an invalid message key %q", entry.Name(), key)
			}
		}
		catalog.Locales[strings.TrimSuffix(entry.Name(), ".json")] = templates
	}
	if _, ok := catalog.Locales["en-US"]; !ok {
		return Catalog{}, fmt.Errorf("plugin: catalog %q has no en-US.json", name)
	}
	return catalog, nil
}

// Resolver 把目录变成进程默认的消息解析器，让 Error.Message 能按区域解析插件自己的消息键。
func (c Catalog) Resolver() primitives.Catalog {
	resolver := primitives.Catalog{}
	for locale, templates := range c.Locales {
		resolver[locale] = templates
	}
	return resolver
}
