import js from "@eslint/js";
import hooks from "eslint-plugin-react-hooks";
import ts from "typescript-eslint";

export default ts.config(
  { ignores: ["dist/**", "node_modules/**"] },
  js.configs.recommended,
  ...ts.configs.recommended,
  hooks.configs.flat["recommended-latest"],
  {
    files: ["src/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            {
              name: "react",
              importNames: ["useMemo", "useCallback"],
              message: "React Compiler owns routine memoization.",
            },
          ],
        },
      ],
    },
  },
);
