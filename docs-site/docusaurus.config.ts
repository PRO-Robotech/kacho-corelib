import { themes as prismThemes } from 'prism-react-renderer'
import type { Config } from '@docusaurus/types'
import type * as Preset from '@docusaurus/preset-classic'

const config: Config = {
  title: 'Kachō Corelib',
  tagline: 'Переиспользуемые горизонтальные Go-пакеты платформы Kachō — id, errors, db, operations, outbox, authz и другие',

  url: 'https://corelib.kacho.cloud',
  baseUrl: '/',
  onBrokenLinks: 'throw',

  organizationName: 'PRO-Robotech',
  projectName: 'kacho-corelib',

  i18n: {
    defaultLocale: 'ru',
    locales: ['ru'],
  },

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  // Продуктовый шрифтовой стек kacho-ui: Inter (текст) + JetBrains Mono (код/значения).
  // Подключаются с Google Fonts; preconnect ускоряет первый запрос.
  stylesheets: [
    {
      href: 'https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600&display=swap',
      type: 'text/css',
    },
  ],
  headTags: [
    {
      tagName: 'link',
      attributes: { rel: 'preconnect', href: 'https://fonts.googleapis.com' },
    },
    {
      tagName: 'link',
      attributes: { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: 'anonymous' },
    },
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themes: ['@docusaurus/theme-mermaid'],

  themeConfig: {
    // Правая in-page оглавление (TOC) — только первый уровень (h2);
    // вложенные подзаголовки в навигации не показываются.
    tableOfContents: {
      minHeadingLevel: 2,
      maxHeadingLevel: 2,
    },
    navbar: {
      title: 'Kachō Corelib',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'corelibSidebar',
          label: 'Документация',
          position: 'left',
        },
        {
          href: 'https://github.com/PRO-Robotech/kacho-corelib',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    colorMode: {
      defaultMode: 'dark',
      disableSwitch: false,
      respectPrefersColorScheme: true,
    },
    footer: {
      style: 'dark',
      copyright: `Copyright © ${new Date().getFullYear()} ООО «ПРТ» · Kachō Cloud Platform.`,
      links: [
        {
          title: 'Документация',
          items: [
            { label: 'Введение', to: '/' },
            { label: 'Быстрый старт', to: '/getting-started' },
            { label: 'Пакеты', to: '/packages/overview' },
            { label: 'Архитектура', to: '/architecture/overview' },
          ],
        },
        {
          title: 'Репозитории',
          items: [
            { label: 'kacho-corelib', href: 'https://github.com/PRO-Robotech/kacho-corelib' },
            { label: 'kacho-proto', href: 'https://github.com/PRO-Robotech/kacho-proto' },
            { label: 'kacho-api-gateway', href: 'https://github.com/PRO-Robotech/kacho-api-gateway' },
          ],
        },
      ],
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'protobuf', 'yaml', 'sql', 'docker', 'go'],
    },
  } satisfies Preset.ThemeConfig,
}

export default config
