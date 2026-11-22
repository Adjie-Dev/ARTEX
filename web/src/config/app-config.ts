import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "ARTEX",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX.`,
  meta: {
    title: "ARTEX - Konsol Pengujian Penetrasi Otonom",
    description: "Konsol sistem pengujian penetrasi otonom yang didukung LLM",
  },
};
