package vsphere

import (
	"fmt"
	"log"
	"path"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"golang.org/x/net/context"
)

type folder struct {
	datacenter   string
	existingPath string
	path         string
}

func resourceVSphereFolder() *schema.Resource {
	return &schema.Resource{
		Create: resourceVSphereFolderCreate,
		Read:   resourceVSphereFolderRead,
		Delete: resourceVSphereFolderDelete,

		Schema: map[string]*schema.Schema{
			"datacenter": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"path": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"existing_path": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceVSphereFolderCreate(d *schema.ResourceData, meta interface{}) error {

	client := meta.(*govmomi.Client)

	f := folder{
		path: strings.TrimRight(d.Get("path").(string), "/"),
	}

	if v, ok := d.GetOk("datacenter"); ok {
		f.datacenter = v.(string)
	}

	err := createFolder(client, &f)
	if err != nil {
		return err
	}

	d.Set("existing_path", f.existingPath)
	d.SetId(fmt.Sprintf("%v/%v", f.datacenter, f.path))
	log.Printf("[INFO] Created folder: %s", f.path)

	return resourceVSphereFolderRead(d, meta)
}

func createFolder(client *govmomi.Client, f *folder) error {

	finder := find.NewFinder(client.Client, false)

	dc, err := finder.DatacenterOrDefault(context.Background(), f.datacenter)
	if err != nil {
		return fmt.Errorf("error %s", err)
	}
	finder = finder.SetDatacenter(dc)
	si := object.NewSearchIndex(client.Client)
	base := filepath.Join(dc.InventoryPath, "vm")
	path := filepath.ToSlash(filepath.Join(base, f.path))

	var folders []string
	var ref object.Reference

	// We iterate over the path starting with full path
	// If we don't find it, we save the folder name and continue with the previous path
	// The iteration ends when we find an existing path otherwise it throws error
	for {
		ref, err = si.FindByInventoryPath(context.Background(), path)
		if err != nil {
			return fmt.Errorf("error %s", err)
		}
		if ref == nil {
			_, folder := filepath.Split(path)
			folders = append(folders, folder)
			path = path[:strings.LastIndex(path, "/")]

			if path == dc.InventoryPath {
				return fmt.Errorf("vSphere base path %s not found", filepath.ToSlash(base))
			}
		} else {
			break
		}
	}

	root := ref.(*object.Folder)
	for i := len(folders) - 1; i >= 0; i-- {
		log.Printf("[DEBUG] folder not found; creating: %s", folders[i])
		root, err = root.CreateFolder(context.Background(), folders[i])
		if err != nil {
			return fmt.Errorf("Failed to create folder at %s; %s", root.InventoryPath, err)
		}
	}
	return nil
}

func resourceVSphereFolderRead(d *schema.ResourceData, meta interface{}) error {

	log.Printf("[DEBUG] reading folder: %#v", d)
	client := meta.(*govmomi.Client)

	dc, err := getDatacenter(client, d.Get("datacenter").(string))
	if err != nil {
		return err
	}

	finder := find.NewFinder(client.Client, true)
	finder = finder.SetDatacenter(dc)

	folder, err := object.NewSearchIndex(client.Client).FindByInventoryPath(
		context.TODO(), fmt.Sprintf("%v/vm/%v", d.Get("datacenter").(string),
			d.Get("path").(string)))

	if err != nil {
		return err
	}

	if folder == nil {
		d.SetId("")
	}

	return nil
}

func resourceVSphereFolderDelete(d *schema.ResourceData, meta interface{}) error {

	f := folder{
		path:         strings.TrimRight(d.Get("path").(string), "/"),
		existingPath: d.Get("existing_path").(string),
	}

	if v, ok := d.GetOk("datacenter"); ok {
		f.datacenter = v.(string)
	}

	client := meta.(*govmomi.Client)

	err := deleteFolder(client, &f)
	if err != nil {
		return err
	}

	d.SetId("")
	return nil
}

func deleteFolder(client *govmomi.Client, f *folder) error {
	dc, err := getDatacenter(client, f.datacenter)
	if err != nil {
		return err
	}
	var folder *object.Folder
	currentPath := f.path

	finder := find.NewFinder(client.Client, true)
	finder = finder.SetDatacenter(dc)
	si := object.NewSearchIndex(client.Client)

	folderRef, err := si.FindByInventoryPath(
		context.TODO(), fmt.Sprintf("%v/vm/%v", f.datacenter, f.path))

	if err != nil {
		return fmt.Errorf("[ERROR] Could not locate folder %s: %v", f.path, err)
	} else {
		folder = folderRef.(*object.Folder)
	}

	log.Printf("[INFO] Deleting empty sub-folders of existing path: %s", f.existingPath)
	for currentPath != f.existingPath {
		log.Printf("[INFO] Deleting folder: %s", currentPath)
		children, err := folder.Children(context.TODO())
		if err != nil {
			return err
		}

		if len(children) > 0 {
			return fmt.Errorf("Folder %s is non-empty and will not be deleted", currentPath)
		} else {
			log.Printf("[DEBUG] current folder: %#v", folder)
			currentPath = path.Dir(currentPath)
			if currentPath == "." {
				currentPath = ""
			}
			log.Printf("[INFO] parent path of %s is calculated as %s", f.path, currentPath)
			task, err := folder.Destroy(context.TODO())
			if err != nil {
				return err
			}
			err = task.Wait(context.TODO())
			if err != nil {
				return err
			}
			folderRef, err = si.FindByInventoryPath(
				context.TODO(), fmt.Sprintf("%v/vm/%v", f.datacenter, currentPath))

			if err != nil {
				return err
			} else if folderRef != nil {
				folder = folderRef.(*object.Folder)
			}
		}
	}
	return nil
}

// getDatacenter gets datacenter object
func getDatacenter(c *govmomi.Client, dc string) (*object.Datacenter, error) {
	finder := find.NewFinder(c.Client, true)
	if dc != "" {
		d, err := finder.Datacenter(context.TODO(), dc)
		return d, err
	} else {
		d, err := finder.DefaultDatacenter(context.TODO())
		return d, err
	}
}
