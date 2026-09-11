const fs = require("node:fs");
const path = require("node:path");

const commonPath = path.resolve(__dirname, "../src/common.ts");
const generated = fs.readFileSync(commonPath, "utf8");
const declaration =
  "export const createRequestFunction = function (axiosArgs: RequestArgs, globalAxios: AxiosInstance, BASE_PATH: string, configuration?: Configuration) {";
const annotatedDeclaration =
  "export const createRequestFunction = function (axiosArgs: RequestArgs, globalAxios: AxiosInstance, BASE_PATH: string, configuration?: Configuration): <T = unknown, R = AxiosResponse<T>>(axios?: AxiosInstance, basePath?: string) => Promise<R> {";
const request = "        return axios.request<T, R>(axiosRequestArgs);";
const compatibleRequest =
  "        return axios.request<T, R>(axiosRequestArgs) as Promise<R>;";

if (!generated.includes(declaration) || !generated.includes(request)) {
  throw new Error(
    "Unable to annotate createRequestFunction: the generated declaration changed. " +
      "Review the TypeScript Axios generator output before updating this patch."
  );
}

fs.writeFileSync(
  commonPath,
  generated
    .replace(declaration, annotatedDeclaration)
    .replace(request, compatibleRequest)
);
