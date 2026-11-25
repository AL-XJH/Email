import eslint from "@eslint/js";
import typescriptEslint from "@typescript-eslint/eslint-plugin";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";

const sourceFiles = ["**/*.{ts,tsx}"];
const forSourceFiles = (config) => ({ ...config, files: sourceFiles });

export default [
  {
    ignores: [
      "dist",
      "node_modules",
      ".next",
      "build",
      "coverage",
      "*.config.js",
      "*.config.ts",
      "vite.config.ts",
      "*.d.ts",
    ],
  },
  forSourceFiles(eslint.configs.recommended),
  ...typescriptEslint.configs["flat/recommended"].map(forSourceFiles),
  forSourceFiles(reactHooks.configs.flat["recommended-latest"]),
  forSourceFiles(reactRefresh.configs.vite),
  {
    files: sourceFiles,
    rules: {
      // 这些规则来自 React Compiler 的新推荐集，现有代码没有接编译器；
      // 保留 rules-of-hooks / exhaustive-deps 这类核心规则，避免把 lint 变成全仓重写。
      "react-hooks/set-state-in-effect": "off",
      "react-hooks/purity": "off",
      "react-hooks/static-components": "off",
      "react-hooks/refs": "off",
      "react-hooks/immutability": "off",
      "react-hooks/globals": "off",
      "react-hooks/error-boundaries": "off",
      "react-hooks/set-state-in-render": "off",
      "react-hooks/preserve-manual-memoization": "off",
      "react-hooks/gating": "off",
      "react-hooks/config": "warn",
    },
  },
  {
    files: ["src/main.tsx"],
    rules: {
      "react-refresh/only-export-components": "off",
    },
  },
  {
    files: sourceFiles,
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
      "@typescript-eslint/no-unused-vars": [
        "warn",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
        },
      ],
      "@typescript-eslint/no-explicit-any": "warn",
    },
  },
  {
    files: ["src/components/ui/badge.tsx"],
    rules: {
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true, allowExportNames: ["badgeVariants"] },
      ],
    },
  },
  {
    files: ["src/components/ui/button.tsx"],
    rules: {
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true, allowExportNames: ["buttonVariants"] },
      ],
    },
  },
  {
    files: ["src/components/ui/sidebar.tsx"],
    rules: {
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true, allowExportNames: ["useSidebar"] },
      ],
    },
  },
  {
    files: ["src/lib/language.tsx"],
    rules: {
      "react-refresh/only-export-components": [
        "warn",
        {
          allowConstantExport: true,
          allowExportNames: [
            "languageOptions",
            "getInitialLanguage",
            "setStoredLanguage",
            "useLanguage",
            "translateUiText",
            "uiText",
            "uiMessage",
            "exactTranslations",
          ],
        },
      ],
    },
  },
  {
    files: ["src/main.tsx"],
    rules: {
      "react-refresh/only-export-components": "off",
    },
  },
];
