"use client";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

import { SettingsForm } from "./settings-form";

export function SettingsTabs({ isAdmin }: { isAdmin: boolean }) {
  if (!isAdmin) return <SettingsForm scope="user" />;
  return (
    <Tabs defaultValue="global">
      <TabsList>
        <TabsTrigger value="global">Platform</TabsTrigger>
        <TabsTrigger value="user">My preferences</TabsTrigger>
      </TabsList>
      <TabsContent value="global" className="pt-4">
        <SettingsForm scope="global" />
      </TabsContent>
      <TabsContent value="user" className="pt-4">
        <SettingsForm scope="user" />
      </TabsContent>
    </Tabs>
  );
}
