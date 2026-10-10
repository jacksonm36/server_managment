import { createContext, useContext } from "react";
import type { Role } from "./api";

export const RoleContext = createContext<Role>("viewer");

export const useRole = () => useContext(RoleContext);
export const isAdmin = (r: Role) => r === "admin";
export const canOperate = (r: Role) => r === "admin" || r === "operator";
