declare module "*.svg" {
  const content: any;
  export default content;
}

declare module "*.png" {
  const content: string;
  export default content;
}

declare module "*.css";

declare module "*/paraglide/messages" {
  const messages: any;
  export = messages;
}
