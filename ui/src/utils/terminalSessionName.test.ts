import { mintSessionName } from "./terminalSessionName";

const taken: string[] = [];
for (let i = 0; i < 100; i++) taken.push(mintSessionName(taken));
if (new Set(taken).size !== 100) throw new Error(`minted a taken name: ${taken.join(" ")}`);
if (!taken.includes("amber")) throw new Error("amber never minted");
let threw = "";
try {
  mintSessionName(taken);
} catch (err) {
  threw = String(err);
}
if (!threw.includes("every terminal session name is taken")) throw new Error(`threw ${threw}`);
console.log("terminalSessionName tests passed");
