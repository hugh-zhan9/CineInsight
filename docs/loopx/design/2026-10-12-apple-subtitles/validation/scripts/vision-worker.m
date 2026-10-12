#import <Foundation/Foundation.h>
#import <Vision/Vision.h>
#import <math.h>
static NSArray *floatArray(NSData *data,NSUInteger n){if(data.length!=n*sizeof(float))return nil;const float *p=data.bytes;NSMutableArray *a=[NSMutableArray arrayWithCapacity:n];for(NSUInteger i=0;i<n;i++){if(!isfinite(p[i]))return nil;[a addObject:@(p[i])];}return a;}
int main(){@autoreleasepool{setbuf(stdout,NULL);char *line=NULL;size_t cap=0;while(getline(&line,&cap,stdin)>0){@autoreleasepool{
 @try{
  NSData *data=[[@(line) stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet] dataUsingEncoding:NSUTF8StringEncoding];
  NSDictionary *input=[NSJSONSerialization JSONObjectWithData:data options:0 error:nil];NSMutableDictionary *result=[NSMutableDictionary dictionaryWithDictionary:@{@"id":input[@"id"]?:@""}];
  NSURL *url=[NSURL fileURLWithPath:input[@"path"]];VNImageRequestHandler *handler=[[VNImageRequestHandler alloc]initWithURL:url options:@{}];NSError *error=nil;
  if([input[@"op"] isEqual:@"face"]){
    VNRequest *request=[[NSClassFromString(@"VNCreateFaceprintRequest") alloc]init];request.usesCPUOnly=YES;
    if([handler performRequests:@[request] error:&error]){
      NSMutableArray *faces=[NSMutableArray array];for(VNFaceObservation *face in request.results){id print=[face valueForKey:@"faceprint"];if(!print)continue;
        NSData *raw=[print valueForKey:@"VNEntityIdentificationModelPrintData"];NSUInteger count=[[print valueForKey:@"VNEntityIdentificationModelPrintElementCount"]unsignedIntegerValue];NSArray *vector=floatArray(raw,count);
        if(vector)[faces addObject:@{@"bbox":@[@(face.boundingBox.origin.x),@(face.boundingBox.origin.y),@(face.boundingBox.size.width),@(face.boundingBox.size.height)],@"quality":@(face.confidence),@"embedding":vector}];
      }result[@"faces"]=faces;
    }
  }else{
    VNGenerateImageFeaturePrintRequest *features=[VNGenerateImageFeaturePrintRequest new];features.usesCPUOnly=YES;
    VNClassifyImageRequest *classes=[VNClassifyImageRequest new];classes.usesCPUOnly=YES;
    if([handler performRequests:@[features,classes] error:&error]){
      VNFeaturePrintObservation *print=features.results.firstObject;result[@"embedding"]=floatArray(print.data,print.elementCount)?:@[];
      NSMutableArray *labels=[NSMutableArray array];for(VNClassificationObservation *c in classes.results){if(c.confidence>=0.02 && labels.count<15)[labels addObject:@{@"label":c.identifier,@"score":@(c.confidence)}];}result[@"labels"]=labels;
    }
  }
  if(error)result[@"error"]=error.localizedDescription;
  NSData *output=[NSJSONSerialization dataWithJSONObject:result options:0 error:nil];fwrite(output.bytes,1,output.length,stdout);puts("");
 }@catch(NSException *e){puts("{\"error\":\"native_exception\"}");}
}}free(line);}}
